// Caché de búsquedas de clases contra Redis 7 real (corrección del 13/11/2026,
// ADR-007 v2): copia por generación, retención, invalidación, pérdida de la
// generación y Redis caído.
package redis

import (
	"context"
	"os/exec"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

func TestClasesRedisReal(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("Docker no disponible")
	}
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{Started: true, ContainerRequest: testcontainers.ContainerRequest{
		Image: "redis:7-alpine", ExposedPorts: []string{"6379/tcp"}, Cmd: []string{"redis-server", "--requirepass", "fixture-redis"},
		WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(time.Minute),
	}})
	t.Cleanup(func() {
		if ctr != nil {
			_ = ctr.Terminate(context.Background())
		}
	})
	require.NoError(t, err)
	addr, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	require.NoError(t, err)
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: "fixture-redis", MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	c := &Clases{Client: client, Retencion: 30 * time.Minute, Timeout: time.Second, Metricas: &MetricasClases{}}

	consultado := time.Date(2026, 11, 13, 13, 0, 0, 0, time.UTC)
	pagina := domain.PaginaClases{Total: 1, Pagina: 1, Tamano: 20, ConsultadoEn: consultado, Items: []domain.Clase{{
		ID: "c1", ActividadID: "a1", ActividadNombre: "Zumba", HorarioID: "h1", Inicio: consultado.Add(time.Hour), Fin: consultado.Add(2 * time.Hour),
		ProfesorID: "p1", ProfesorNombre: "María Pérez", Capacidad: 30, Ocupacion: 12, Estado: domain.ClaseProgramada, Version: 4, ReservadaPorMi: true,
	}}}

	_, g, ok, err := c.LeerClases(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok, "vacía es faltante")
	require.NotEmpty(t, g, "la generación se crea al leer")
	require.EqualValues(t, 1, c.Metricas.Faltantes.Load())

	require.NoError(t, c.GuardarClases(ctx, g, "k", pagina))
	leida, g2, ok, err := c.LeerClases(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, g, g2)
	esperada := pagina
	esperada.Items = []domain.Clase{pagina.Items[0]}
	esperada.Items[0].ReservadaPorMi = false
	require.Equal(t, esperada, leida, "la copia conserva todo menos la marca del alumno")
	ttl, err := client.TTL(ctx, prefijoClases+g+":k").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 29*time.Minute, "vive la retención configurada")

	require.NoError(t, c.InvalidarClases(ctx))
	_, g3, ok, err := c.LeerClases(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok, "invalidar cambia la generación")
	require.NotEqual(t, g, g3)

	// Una copia guardada con la generación anterior queda inalcanzable.
	require.NoError(t, c.GuardarClases(ctx, g, "k", pagina))
	_, _, ok, err = c.LeerClases(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok, "una escritura tardía de otra generación no se lee")

	// Si Redis pierde la generación, se crea otra y no reaparecen copias viejas.
	require.NoError(t, c.GuardarClases(ctx, g3, "k", pagina))
	require.NoError(t, client.Del(ctx, ClaveGeneracionClases).Err())
	_, g4, ok, err := c.LeerClases(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok)
	require.NotEqual(t, g3, g4)

	// La última copia sobrevive a la invalidación y vive la misma retención; solo
	// la usa la aplicación cuando la fuente no responde (jornada, D-95).
	ultima, ok, err := c.LeerUltimaClases(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok, "la última copia no depende de la generación")
	require.Equal(t, esperada, ultima)
	ttl, err = client.TTL(ctx, prefijoUltima+"k").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 29*time.Minute)
	_, ok, err = c.LeerUltimaClases(ctx, "otra")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, client.Set(ctx, prefijoClases+g4+":roto", "{roto", time.Minute).Err())
	_, _, ok, err = c.LeerClases(ctx, "roto")
	require.NoError(t, err)
	require.False(t, ok, "una copia ilegible es faltante")

	errores := c.Metricas.Errores.Load()
	require.NoError(t, ctr.Stop(ctx, nil))
	_, _, ok, err = c.LeerClases(ctx, "k")
	require.Error(t, err, "Redis caído es error, no faltante")
	require.False(t, ok)
	require.Greater(t, c.Metricas.Errores.Load(), errores)
}
