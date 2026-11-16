package amqp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

// ErrRevocacionInvalida es permanente: el mensaje va a la DLQ sin reintentos.
var ErrRevocacionInvalida = errors.New("evento de members inválido")

// PermanentRevocacion clasifica errores del consumidor de bajas.
func PermanentRevocacion(err error) bool {
	return errors.Is(err, ErrRevocacionInvalida) || errors.Is(err, domain.ErrValidacion)
}

// Revocador es el puerto de entrada que aplica una baja (application.Servicio).
type Revocador interface {
	Revocar(context.Context, domain.Revocacion) (int, error)
}

// Revocaciones traduce membresia.cancelada y usuario.desactivado
// (docs/events/*.v1.json) a una revocación de dominio (RN-04, RN-34, D-98).
type Revocaciones struct {
	App       Revocador
	EventType string
}

func (a Revocaciones) Handle(ctx context.Context, body []byte) error {
	var s sobre
	if len(body) > 64<<10 || json.Unmarshal(body, &s) != nil || s.EventVersion != 1 || s.Producer != "members-service" || s.EventType != a.EventType || s.OccurredAt.IsZero() {
		return ErrRevocacionInvalida
	}
	r := domain.Revocacion{EventoID: s.EventID, OcurridaEn: s.OccurredAt.UTC()}
	switch s.EventType {
	case "membresia.cancelada":
		var d struct {
			MembresiaID string `json:"membresia_id"`
			AlumnoID    string `json:"alumno_id"`
		}
		if json.Unmarshal(s.Data, &d) != nil {
			return ErrRevocacionInvalida
		}
		r.Tipo, r.AlumnoID, r.MembresiaID = domain.RevocacionMembresiaCancelada, d.AlumnoID, d.MembresiaID
	case "usuario.desactivado":
		var d struct {
			UsuarioID string `json:"usuario_id"`
			Rol       string `json:"rol"`
		}
		if json.Unmarshal(s.Data, &d) != nil || d.Rol == "" {
			return ErrRevocacionInvalida
		}
		if d.Rol != "ALUMNO" {
			// Solo los alumnos tienen reservas. La regla de no dar de baja a un
			// profesor con clases futuras (RN-34) no se resuelve con este evento.
			return nil
		}
		r.Tipo, r.AlumnoID = domain.RevocacionUsuarioDesactivado, d.UsuarioID
	default:
		return ErrRevocacionInvalida
	}
	_, err := a.App.Revocar(ctx, r)
	return err
}
