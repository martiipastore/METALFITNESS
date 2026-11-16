// D-98: traducción de membresia.cancelada y usuario.desactivado (RN-04, RN-34).
package amqp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

type revocadorFalso struct {
	recibidas []domain.Revocacion
	err       error
}

func (r *revocadorFalso) Revocar(_ context.Context, rev domain.Revocacion) (int, error) {
	r.recibidas = append(r.recibidas, rev)
	return 0, r.err
}

const (
	cancelada = `{"event_id":"00000000-0000-4000-8000-000000000001","event_type":"membresia.cancelada","event_version":1,"occurred_at":"2026-11-15T20:00:00Z","producer":"members-service",
	"data":{"membresia_id":"00000000-0000-4000-8000-000000000002","alumno_id":"00000000-0000-4000-8000-000000000003","motivo":"pedido del alumno"}}`
	desactivado = `{"event_id":"00000000-0000-4000-8000-000000000004","event_type":"usuario.desactivado","event_version":1,"occurred_at":"2026-11-15T20:00:00Z","producer":"members-service",
	"data":{"usuario_id":"00000000-0000-4000-8000-000000000003","rol":"ALUMNO","desactivado_en":"2026-11-15T20:00:00Z"}}`
)

func TestRevocacionesTraduceEventosDeMembers(t *testing.T) {
	app := &revocadorFalso{}
	require.NoError(t, Revocaciones{App: app, EventType: "membresia.cancelada"}.Handle(context.Background(), []byte(cancelada)))
	require.NoError(t, Revocaciones{App: app, EventType: "usuario.desactivado"}.Handle(context.Background(), []byte(desactivado)))
	require.Len(t, app.recibidas, 2)
	require.Equal(t, domain.Revocacion{EventoID: "00000000-0000-4000-8000-000000000001", Tipo: domain.RevocacionMembresiaCancelada,
		AlumnoID: "00000000-0000-4000-8000-000000000003", MembresiaID: "00000000-0000-4000-8000-000000000002", OcurridaEn: app.recibidas[0].OcurridaEn}, app.recibidas[0])
	require.Equal(t, domain.RevocacionUsuarioDesactivado, app.recibidas[1].Tipo)
	require.Empty(t, app.recibidas[1].MembresiaID)
}

func TestRevocacionesIgnoraBajasQueNoSonDeAlumnos(t *testing.T) {
	app := &revocadorFalso{}
	profesor := []byte(`{"event_id":"00000000-0000-4000-8000-000000000005","event_type":"usuario.desactivado","event_version":1,"occurred_at":"2026-11-15T20:00:00Z","producer":"members-service","data":{"usuario_id":"00000000-0000-4000-8000-000000000006","rol":"PROFESOR"}}`)
	require.NoError(t, Revocaciones{App: app, EventType: "usuario.desactivado"}.Handle(context.Background(), profesor))
	require.Empty(t, app.recibidas, "un profesor no tiene reservas: se confirma sin efecto")
}

func TestRevocacionesRechazaSobresInvalidos(t *testing.T) {
	app := &revocadorFalso{}
	h := Revocaciones{App: app, EventType: "membresia.cancelada"}
	for _, body := range []string{
		`no es json`,
		desactivado, // otra routing key
		`{"event_id":"x","event_type":"membresia.cancelada","event_version":2,"occurred_at":"2026-11-15T20:00:00Z","producer":"members-service","data":{}}`,
		`{"event_id":"x","event_type":"membresia.cancelada","event_version":1,"occurred_at":"2026-11-15T20:00:00Z","producer":"otro","data":{}}`,
		`{"event_id":"x","event_type":"membresia.cancelada","event_version":1,"producer":"members-service","data":{}}`,
	} {
		err := h.Handle(context.Background(), []byte(body))
		require.ErrorIs(t, err, ErrRevocacionInvalida, body)
		require.True(t, PermanentRevocacion(err), "un sobre inválido va directo a la DLQ")
	}
	require.Empty(t, app.recibidas)
	require.True(t, PermanentRevocacion(domain.ErrValidacion))
	require.False(t, PermanentRevocacion(errors.New("members caído")), "una falla transitoria se reintenta")
}
