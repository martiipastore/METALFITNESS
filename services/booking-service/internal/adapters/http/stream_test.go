// D-103: cupos en vivo por Server-Sent Events.
package http

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/sse"
)

func servidorStream(t *testing.T, hub *sse.Hub, latido, duracion time.Duration) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterStream(r, hub, strings.Repeat("s", 32), latido, duracion)
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	return s
}

func pedirStream(t *testing.T, ctx context.Context, url string, identidad bool) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/classes/stream", nil)
	require.NoError(t, err)
	if identidad {
		req.Header.Set("X-Internal-Service-Key", strings.Repeat("s", 32))
		req.Header.Set("X-User-Id", "00000000-0000-4000-8000-000000000001")
		req.Header.Set("X-User-Role", "ALUMNO")
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return res
}

func TestStreamEntregaCambiosYLatidos(t *testing.T) {
	hub := sse.NuevoHub(10)
	s := servidorStream(t, hub, 50*time.Millisecond, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := pedirStream(t, ctx, s.URL, true)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	require.Equal(t, "no-store", res.Header.Get("Cache-Control"))

	lector := bufio.NewReader(res.Body)
	require.Eventually(t, func() bool { return hub.Conectados() == 1 }, time.Second, 10*time.Millisecond)
	hub.Publicar(sse.Cambio{ClaseID: "c1", Ocupacion: 3, Capacidad: 20, Estado: "PROGRAMADA", Version: 7})
	var evento, data string
	latido := false
	for evento == "" || data == "" {
		linea, err := lector.ReadString('\n')
		require.NoError(t, err)
		switch {
		case strings.HasPrefix(linea, "event: "):
			evento = strings.TrimSpace(strings.TrimPrefix(linea, "event: "))
		case strings.HasPrefix(linea, "data: "):
			data = strings.TrimSpace(strings.TrimPrefix(linea, "data: "))
		case strings.HasPrefix(linea, ": latido"):
			latido = true
		}
	}
	require.Equal(t, "ocupacion", evento)
	require.JSONEq(t, `{"clase_id":"c1","ocupacion":3,"capacidad":20,"estado":"PROGRAMADA","version":7}`, data)
	for !latido {
		linea, err := lector.ReadString('\n')
		require.NoError(t, err)
		latido = strings.HasPrefix(linea, ": latido")
	}
	cancel()
	require.Eventually(t, func() bool { return hub.Conectados() == 0 }, time.Second, 10*time.Millisecond, "al cortar el cliente se libera la suscripción")
}

func TestStreamExigeIdentidadYRespetaElMaximo(t *testing.T) {
	hub := sse.NuevoHub(1)
	s := servidorStream(t, hub, time.Minute, time.Minute)
	res := pedirStream(t, context.Background(), s.URL, false)
	require.Equal(t, 401, res.StatusCode, "sin la identidad del gateway no se abre")
	res.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	primero := pedirStream(t, ctx, s.URL, true)
	defer primero.Body.Close()
	require.Eventually(t, func() bool { return hub.Conectados() == 1 }, time.Second, 10*time.Millisecond)
	segundo := pedirStream(t, context.Background(), s.URL, true)
	require.Equal(t, 503, segundo.StatusCode, "con el máximo de conexiones responde 503")
	require.Equal(t, "30", segundo.Header.Get("Retry-After"))
	segundo.Body.Close()
}

func TestStreamTerminaALaDuracionMaxima(t *testing.T) {
	hub := sse.NuevoHub(10)
	s := servidorStream(t, hub, time.Minute, 100*time.Millisecond)
	res := pedirStream(t, context.Background(), s.URL, true)
	defer res.Body.Close()
	cuerpo := make([]byte, 0, 256)
	lector := bufio.NewReader(res.Body)
	for {
		linea, err := lector.ReadString('\n')
		cuerpo = append(cuerpo, linea...)
		if err != nil {
			break
		}
	}
	require.Contains(t, string(cuerpo), "event: fin", "avisa el cierre para que el navegador se reconecte")
	require.Contains(t, string(cuerpo), "retry: 3000")
}

func TestHubDescartaParaUnSuscriptorLento(t *testing.T) {
	hub := sse.NuevoHub(10)
	_, baja, ok := hub.Suscribir()
	require.True(t, ok)
	defer baja()
	for i := 0; i < 40; i++ {
		hub.Publicar(sse.Cambio{ClaseID: "c", Version: int64(i)})
	}
	enviados, descartados := hub.Contadores()
	require.Equal(t, int64(32), enviados, "el búfer guarda 32")
	require.Equal(t, int64(8), descartados, "el resto se descarta sin bloquear")
}
