package amqp

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/messaging"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/sse"
)

var errDesconectado = errors.New("cupos en vivo sin conexión al broker")

// Ocupacion escucha clase.actualizada para los cupos en vivo (D-103). Cada
// instancia de Booking declara su propia cola temporal (exclusiva, sin
// durabilidad, se borra al desconectarse) atada a gym.events: así un cambio
// hecho en una instancia llega a los navegadores conectados a la otra. No usa
// reintentos ni DLQ: perder un aviso solo atrasa la pantalla hasta la próxima
// recarga, y la reserva siempre se valida en PostgreSQL.
type Ocupacion struct {
	URL       string
	Hub       *sse.Hub
	Log       *slog.Logger
	Conectado atomic.Bool
}

// Ready informa si la suscripción está activa (dependencia parcial).
func (o *Ocupacion) Ready(context.Context) error {
	if !o.Conectado.Load() {
		return errDesconectado
	}
	return nil
}

// Run mantiene la suscripción y se reconecta si se cae el broker.
func (o *Ocupacion) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := o.escuchar(ctx); err != nil && ctx.Err() == nil {
			o.Log.Warn("cupos en vivo sin conexión al broker; se reintentará")
		}
		o.Conectado.Store(false)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (o *Ocupacion) escuchar(ctx context.Context) error {
	bus, err := messaging.Dial(ctx, o.URL)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err = bus.Exchanges(); err != nil {
		return err
	}
	q, err := bus.Channel.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		return err
	}
	if err = bus.Channel.QueueBind(q.Name, "clase.actualizada", "gym.events", false, nil); err != nil {
		return err
	}
	entregas, err := bus.Channel.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		return err
	}
	o.Conectado.Store(true)
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-entregas:
			if !ok {
				return errDesconectado
			}
			e, err := traducir(d.Body)
			if err != nil {
				continue // un sobre inválido lo descarta el indexer en su propia cola
			}
			o.Hub.Publicar(sse.Cambio{ClaseID: e.Clase.ID, Ocupacion: e.Clase.Ocupacion, Capacidad: e.Clase.Capacidad, Estado: e.Clase.Estado, Version: e.Clase.Version})
		}
	}
}
