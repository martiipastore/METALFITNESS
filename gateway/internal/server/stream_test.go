// D-103: el stream de cupos en vivo no lleva el timeout de su ruta; el resto de
// /api/v1/classes sí.
package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/access"
	"github.com/stretchr/testify/require"
)

func TestStreamDeCuposSinTimeoutDeRuta(t *testing.T) {
	booking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/classes/stream" {
			time.Sleep(400 * time.Millisecond) // una búsqueda lenta: control
			w.WriteHeader(200)
			return
		}
		require.Equal(t, access.Student, r.Header.Get("X-User-Role"), "el stream sigue autenticado por el gateway")
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 4; i++ {
			fmt.Fprintf(w, "event: ocupacion\ndata: {\"version\":%d}\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer booking.Close()
	f := newFixture(t, map[string]string{"GATEWAY_BOOKING_URL": booking.URL, "GATEWAY_TIMEOUT_CLASSES": "150ms", "GYM_INTERNAL_SERVICE_KEY": strings.Repeat("s", 32)})
	alumno := map[string]string{"Authorization": "Bearer " + testJWT(t, access.Student, time.Now())}

	res := f.do("GET", "/api/v1/classes/stream", alumno)
	require.Equal(t, 200, res.Code)
	require.Equal(t, 4, strings.Count(res.Body.String(), "event: ocupacion"), "los 4 eventos llegan aunque el stream dure más que el timeout de la ruta")

	require.Equal(t, 504, f.do("GET", "/api/v1/classes", alumno).Code, "una búsqueda lenta sigue cortándose")
	require.Equal(t, 401, f.do("GET", "/api/v1/classes/stream", nil).Code, "sin token no hay stream")
}
