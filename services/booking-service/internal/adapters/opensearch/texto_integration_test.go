// Búsqueda por texto parcial contra OpenSearch 2 real (corrección del
// 13/11/2026, HU-13): campo texto analizado, prefijos por palabra y
// reconstrucción de un índice anterior al campo.
package opensearch

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

func TestBusquedaTextoOpenSearch(t *testing.T) {
	c := nuevoOpenSearch(t)
	ctx := context.Background()

	// Un índice creado con el mapping anterior no tiene el campo texto.
	viejo := `{"mappings":{"dynamic":"strict","properties":{"id":{"type":"keyword"}}}}`
	status, err := c.do(ctx, http.MethodPut, "/clases-viejo", viejo, nil)
	require.NoError(t, err)
	require.Equal(t, 200, status)
	_, err = c.Apuntar(ctx, "clases-viejo")
	require.NoError(t, err)
	vigente, err := c.Vigente(ctx)
	require.NoError(t, err)
	require.False(t, vigente, "el índice viejo debe reconstruirse")

	require.NoError(t, c.CrearIndice(ctx, "clases-texto"))
	_, err = c.Apuntar(ctx, "clases-texto")
	require.NoError(t, err)
	vigente, err = c.Vigente(ctx)
	require.NoError(t, err)
	require.True(t, vigente)

	dia := time.Now().In(domain.Zona).AddDate(0, 0, 2).Format(time.DateOnly)
	nueva := func(n int, actividad, profesor string, hora int) domain.Clase {
		cl := claseN(n, uuidN(100+n), zona(t, dia, hora, 0), 10, 0)
		cl.ActividadNombre, cl.ProfesorNombre = actividad, profesor
		return cl
	}
	zumba := nueva(1, "Zumba", "María Pérez", 9)
	muscu := nueva(2, "Musculación", "Juan Ruiz", 10)
	strong := nueva(3, "Strong Nation", "Lucía Gómez", 11)
	for _, cl := range []domain.Clase{zumba, muscu, strong} {
		ok, err := c.Indexar(ctx, cl)
		require.NoError(t, err)
		require.True(t, ok)
	}
	refrescar(t, c)

	desde, err := domain.ParseFecha(dia)
	require.NoError(t, err)
	buscar := func(texto string) []string {
		t.Helper()
		palabras, err := domain.PalabrasBusqueda(texto)
		require.NoError(t, err)
		page, err := c.Buscar(ctx, domain.FiltroClases{Desde: desde, Hasta: desde.AddDate(0, 0, 1), Palabras: palabras, Pagina: 1, Tamano: 20}, time.Now())
		require.NoError(t, err)
		require.Equal(t, len(page.Items), page.Total)
		return ids(page)
	}

	require.Equal(t, []string{zumba.ID}, buscar("zum"), "prefijo de la actividad")
	require.Equal(t, []string{zumba.ID}, buscar("Z"), "una sola letra")
	require.Equal(t, []string{muscu.ID}, buscar("MUSCULACIÓN"), "sin distinguir mayúsculas ni tildes")
	require.Equal(t, []string{strong.ID}, buscar("nat"), "prefijo de la segunda palabra")
	require.Equal(t, []string{zumba.ID}, buscar("mari"), "prefijo del profesor")
	require.Equal(t, []string{strong.ID}, buscar("str gom"), "todas las palabras deben coincidir")
	require.Empty(t, buscar("umba"), "solo comienzos de palabra, igual que PostgreSQL")
	require.Len(t, buscar(""), 3, "sin texto no filtra")
}
