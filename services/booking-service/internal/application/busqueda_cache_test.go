// Corrección de búsqueda del 13/11/2026 (ADR-007 v2, HU-13): una búsqueda de
// clases se guarda en la caché y puede responderse con PostgreSQL apagado.
package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/ports"
)

// catalogoBuscable cuenta las búsquedas en PostgreSQL y puede simular la base caída.
type catalogoBuscable struct {
	catalogo
	busquedas int
	alumno    string
	caida     bool
	filtro    domain.FiltroClases
}

func (c *catalogoBuscable) BuscarClases(_ context.Context, f domain.FiltroClases, alumnoID string) (domain.PaginaClases, error) {
	c.busquedas++
	c.alumno, c.filtro = alumnoID, f
	if c.caida {
		return domain.PaginaClases{}, errors.New("conexión rechazada")
	}
	return domain.PaginaClases{Items: []domain.Clase{{ID: "zumba-lunes", ActividadNombre: "Zumba"}, {ID: "zumba-martes", ActividadNombre: "Zumba"}}, Total: 2, Pagina: f.Pagina, Tamano: f.Tamano}, nil
}

// cacheClasesFalsa guarda por generación como el adaptador de Redis.
type cacheClasesFalsa struct {
	copias         map[string]domain.PaginaClases
	generacion     int
	caida          bool
	guardadas      int
	invalidadas    int
	ultimaGuardada domain.PaginaClases
}

func nuevaCacheClases() *cacheClasesFalsa {
	return &cacheClasesFalsa{copias: map[string]domain.PaginaClases{}}
}
func (c *cacheClasesFalsa) gen() string { return string(rune('a' + c.generacion)) }
func (c *cacheClasesFalsa) LeerClases(_ context.Context, clave string) (domain.PaginaClases, string, bool, error) {
	if c.caida {
		return domain.PaginaClases{}, "", false, errors.New("redis caído")
	}
	p, ok := c.copias[c.gen()+clave]
	return p, c.gen(), ok, nil
}
func (c *cacheClasesFalsa) GuardarClases(_ context.Context, g, clave string, p domain.PaginaClases) error {
	if c.caida {
		return errors.New("redis caído")
	}
	c.copias[g+clave] = p
	c.copias["ultima:"+clave] = p
	c.guardadas++
	c.ultimaGuardada = p
	return nil
}
func (c *cacheClasesFalsa) LeerUltimaClases(_ context.Context, clave string) (domain.PaginaClases, bool, error) {
	if c.caida {
		return domain.PaginaClases{}, false, errors.New("redis caído")
	}
	p, ok := c.copias["ultima:"+clave]
	return p, ok, nil
}
func (c *cacheClasesFalsa) InvalidarClases(context.Context) error {
	if c.caida {
		return errors.New("redis caído")
	}
	c.generacion++
	c.invalidadas++
	return nil
}

type relojMovil struct{ t *time.Time }

func (r relojMovil) Ahora() time.Time { return *r.t }

func TestBusquedaConCache(t *testing.T) {
	ctx := context.Background()
	ahora := time.Date(2026, 11, 13, 10, 0, 0, 0, domain.Zona)
	base := &catalogoBuscable{}
	cache := nuevaCacheClases()
	marcas := &marcasFalsas{}
	s := New(base, &reservas{}, members{}, relojMovil{&ahora}).ConCacheClases(cache, marcas, 30*time.Second)
	yo := Actor{ID: alumno, Rol: RolAlumno}
	filtro := domain.FiltroClases{Texto: "zum", Pagina: 1, Tamano: 20}

	t.Run("primera_busqueda_va_a_postgres_y_guarda_sin_marcas", func(t *testing.T) {
		page, err := s.BuscarClases(ctx, yo, filtro)
		if err != nil || base.busquedas != 1 || cache.guardadas != 1 || page.Origen != domain.OrigenFuente {
			t.Fatalf("err=%v busquedas=%d guardadas=%d origen=%s", err, base.busquedas, cache.guardadas, page.Origen)
		}
		if base.alumno != "" {
			t.Fatal("la copia compartida no puede depender del alumno")
		}
		for _, c := range cache.ultimaGuardada.Items {
			if c.ReservadaPorMi {
				t.Fatal("la copia guardada no lleva marcas de un alumno")
			}
		}
		if !page.Items[0].ReservadaPorMi || !page.MarcasDisponibles {
			t.Fatal("la respuesta al alumno sí lleva sus marcas")
		}
		if len(base.filtro.Palabras) != 1 || base.filtro.Palabras[0] != "zum" {
			t.Fatalf("la búsqueda parcial llega normalizada a PostgreSQL: %v", base.filtro.Palabras)
		}
	})
	t.Run("con_postgres_apagado_responde_desde_la_cache", func(t *testing.T) {
		base.caida = true
		marcas.err = errors.New("base caída")
		page, err := s.BuscarClases(ctx, yo, filtro)
		if err != nil {
			t.Fatalf("debe responder desde la caché: %v", err)
		}
		if base.busquedas != 1 || page.Origen != domain.OrigenCache || len(page.Items) != 2 {
			t.Fatalf("busquedas=%d origen=%s items=%d", base.busquedas, page.Origen, len(page.Items))
		}
		if page.MarcasDisponibles || page.Items[0].ReservadaPorMi {
			t.Fatal("sin base no puede afirmar qué reservó el alumno")
		}
	})
	t.Run("copia_vencida_se_sirve_si_la_fuente_sigue_caida", func(t *testing.T) {
		ahora = ahora.Add(5 * time.Minute)
		page, err := s.BuscarClases(ctx, yo, filtro)
		if err != nil || page.Origen != domain.OrigenCacheVencida {
			t.Fatalf("err=%v origen=%s", err, page.Origen)
		}
		if !page.ConsultadoEn.Equal(ahora.Add(-5 * time.Minute)) {
			t.Fatalf("debe conservar cuándo se consultó la fuente: %v", page.ConsultadoEn)
		}
	})
	t.Run("copia_vencida_se_refresca_cuando_la_fuente_vuelve", func(t *testing.T) {
		base.caida, marcas.err = false, nil
		page, err := s.BuscarClases(ctx, yo, filtro)
		if err != nil || page.Origen != domain.OrigenFuente || !page.ConsultadoEn.Equal(ahora) || base.busquedas != 3 {
			t.Fatalf("err=%v origen=%s consultado=%v busquedas=%d", err, page.Origen, page.ConsultadoEn, base.busquedas)
		}
	})
	t.Run("sin_copia_y_sin_fuente_es_503", func(t *testing.T) {
		base.caida = true
		defer func() { base.caida = false }()
		_, err := s.BuscarClases(ctx, yo, domain.FiltroClases{Texto: "funcional", Pagina: 1, Tamano: 20})
		if !errors.Is(err, ports.ErrNoDisponible) {
			t.Fatalf("debe ser dependencia no disponible: %v", err)
		}
	})
	t.Run("otro_filtro_es_otra_clave", func(t *testing.T) {
		antes := base.busquedas
		if _, err := s.BuscarClases(ctx, yo, domain.FiltroClases{Texto: "zum", Pagina: 2, Tamano: 20}); err != nil || base.busquedas != antes+1 {
			t.Fatalf("otra página no puede usar la copia de la primera: err=%v", err)
		}
	})
	t.Run("redis_caido_lee_la_fuente_sin_guardar", func(t *testing.T) {
		cache.caida = true
		defer func() { cache.caida = false }()
		antes, guardadas := base.busquedas, cache.guardadas
		page, err := s.BuscarClases(ctx, yo, filtro)
		if err != nil || page.Origen != domain.OrigenFuente || base.busquedas != antes+1 || cache.guardadas != guardadas {
			t.Fatalf("err=%v origen=%s", err, page.Origen)
		}
	})
}

// marcasColgadas simula PostgreSQL detenido: la consulta no falla, espera.
type marcasColgadas struct{}

func (marcasColgadas) ReservadasPor(ctx context.Context, _ string, _ []string) (map[string]bool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestMarcasConBaseColgadaNoDemoranLaBusqueda(t *testing.T) {
	ahora := time.Date(2026, 11, 13, 10, 0, 0, 0, domain.Zona)
	s := New(&catalogoBuscable{}, &reservas{}, members{}, relojMovil{&ahora}).ConCacheClases(nuevaCacheClases(), marcasColgadas{}, 30*time.Second)
	inicio := time.Now()
	page, err := s.BuscarClases(context.Background(), Actor{ID: alumno, Rol: RolAlumno}, domain.FiltroClases{Pagina: 1, Tamano: 20})
	demora := time.Since(inicio)
	if err != nil || page.MarcasDisponibles || len(page.Items) != 2 {
		t.Fatalf("debe responder sin marcas: err=%v marcas=%v", err, page.MarcasDisponibles)
	}
	if demora > TiempoMaximoMarcas+500*time.Millisecond {
		t.Fatalf("la marca colgada demoró la búsqueda %v; el gateway corta a los 3 s", demora)
	}
}

func TestEscriturasInvalidanBusquedas(t *testing.T) {
	s, _, res, _, _ := escenario(t)
	cache := nuevaCacheClases()
	s.ConCacheClases(cache, &marcasFalsas{}, 30*time.Second)
	ctx := context.Background()
	yo := Actor{ID: alumno, Rol: RolAlumno}

	if _, _, err := s.Reservar(ctx, yo, clave, claseID); err != nil {
		t.Fatal(err)
	}
	if cache.invalidadas != 1 {
		t.Fatalf("una reserva cambia la ocupación: invalidadas=%d", cache.invalidadas)
	}
	res.previa, res.reserva = true, domain.Reserva{ID: "r"}
	if _, repetida, err := s.Reservar(ctx, yo, clave, claseID); err != nil || !repetida || cache.invalidadas != 1 {
		t.Fatalf("un replay idempotente no cambia nada: repetida=%v invalidadas=%d err=%v", repetida, cache.invalidadas, err)
	}
}

// Hallazgo de la jornada de fallas (15/11/2026): con OpenSearch caído y
// PostgreSQL arriba, cada reserva cambia la generación y la copia vigente queda
// inalcanzable. La última copia conocida evita responder 503 (ADR-007 v2, D-95).
func TestCopiaVencidaSobreviveALaInvalidacion(t *testing.T) {
	ctx := context.Background()
	ahora := time.Date(2026, 11, 16, 10, 0, 0, 0, domain.Zona)
	base := &catalogoBuscable{}
	cache := nuevaCacheClases()
	s := New(base, &reservas{}, members{}, relojMovil{&ahora}).ConCacheClases(cache, &marcasFalsas{}, 30*time.Second)
	yo := Actor{ID: alumno, Rol: RolAlumno}
	filtro := domain.FiltroClases{Texto: "zum", Pagina: 1, Tamano: 20}

	if _, err := s.BuscarClases(ctx, yo, filtro); err != nil {
		t.Fatal(err)
	}
	cache.InvalidarClases(ctx) // una reserva confirmada
	base.caida = true
	page, err := s.BuscarClases(ctx, yo, filtro)
	if err != nil || page.Origen != domain.OrigenCacheVencida || len(page.Items) == 0 {
		t.Fatalf("la última copia se sirve como vencida: origen=%s err=%v", page.Origen, err)
	}
	base.caida = false
	if page, err = s.BuscarClases(ctx, yo, filtro); err != nil || page.Origen != domain.OrigenFuente {
		t.Fatalf("con la fuente arriba nunca se usa la última copia: origen=%s err=%v", page.Origen, err)
	}
	cache.copias = map[string]domain.PaginaClases{}
	cache.InvalidarClases(ctx)
	base.caida = true
	if _, err = s.BuscarClases(ctx, yo, filtro); err == nil {
		t.Fatal("sin ninguna copia y sin fuente, la búsqueda falla (503)")
	}
}
