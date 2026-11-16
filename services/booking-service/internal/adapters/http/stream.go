package http

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/access"
	"github.com/GuadiViti/Arquitectura-de-Software/pkg/problem"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/sse"
)

// Latido mantiene viva la conexión a través de proxies; DuracionStream la corta
// para que el navegador se reconecte y vuelva a balancearse entre instancias.
const (
	Latido         = 15 * time.Second
	DuracionStream = 10 * time.Minute
)

// RegisterStream expone GET /api/v1/classes/stream con Server-Sent Events: los
// cambios de ocupación de las clases (bonus D-40, D-103). Exige la misma
// identidad que el resto de la API (la inyecta el gateway), pero va fuera del
// grupo con timeout de 4 s porque la conexión queda abierta.
func RegisterStream(r *gin.Engine, hub *sse.Hub, secret string, latido, duracion time.Duration) {
	r.GET("/api/v1/classes/stream", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if len(secret) < 32 || len(c.Request.Header.Values("X-Internal-Service-Key")) != 1 ||
			subtle.ConstantTimeCompare([]byte(secret), []byte(c.GetHeader("X-Internal-Service-Key"))) != 1 ||
			len(c.Request.Header.Values("X-User-Id")) != 1 || !access.ValidRole(c.GetHeader("X-User-Role")) {
			problem.Write(c, problem.New(401, "NO_AUTENTICADO", "No autorizado", "Ingresá para acceder."))
			return
		}
		if len(c.Request.URL.Query()) != 0 {
			problem.Write(c, problem.New(400, problem.CodeValidation, "Solicitud inválida", "El stream no acepta parámetros."))
			return
		}
		cambios, baja, ok := hub.Suscribir()
		if !ok {
			c.Header("Retry-After", "30")
			problem.Write(c, problem.New(503, problem.CodeDependencyUnavailable, "Servicio no disponible", "Demasiadas conexiones en vivo. La lista se actualiza igual cada 30 s."))
			return
		}
		defer baja()

		h := c.Writer.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		c.Status(200)
		// retry indica al cliente cuánto esperar antes de reconectarse.
		fmt.Fprint(c.Writer, "retry: 3000\n: conectado\n\n")
		c.Writer.Flush()

		pulso := time.NewTicker(latido)
		defer pulso.Stop()
		fin := time.NewTimer(duracion)
		defer fin.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-pulso.C:
				fmt.Fprint(c.Writer, ": latido\n\n")
			case <-fin.C:
				fmt.Fprint(c.Writer, "event: fin\ndata: {}\n\n")
				c.Writer.Flush()
				return
			case cambio := <-cambios:
				data, err := json.Marshal(cambio)
				if err != nil {
					continue
				}
				fmt.Fprintf(c.Writer, "event: ocupacion\ndata: %s\n\n", data)
			}
			c.Writer.Flush()
		}
	})
}
