package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/messaging"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Tablas de outbox y eventos que tienen consumidor. Las demás routing keys
// (membresia.vencida, alumno.creado) quedan pendientes hasta que exista su
// consumidor; no se descartan.
var outboxes = []struct{ tabla, tipos string }{
	// membresia.activada → notification-worker (RN-05); membresia.cancelada →
	// booking (RN-04, D-98).
	{"outbox_membresias", "'membresia.activada','membresia.cancelada'"},
	// usuario.desactivado → booking (RN-34, D-98).
	{"outbox_usuarios", "'usuario.desactivado'"},
}

// RelayMemberships publica los eventos pendientes de los dos outbox de Members.
// El bloqueo permanece hasta el confirm y el commit. Una caída posterior al
// confirm puede repetir el event_id: el consumidor debe deduplicar.
func (p *Postgres) RelayMemberships(ctx context.Context, bus *messaging.Session) (int, error) {
	total := 0
	for _, o := range outboxes {
		n, err := p.relay(ctx, bus, o.tabla, o.tipos)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (p *Postgres) relay(ctx context.Context, bus *messaging.Session, tabla, tipos string) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// tabla y tipos son constantes de este archivo, nunca datos externos. Nombre
	// y email solo viajan en membresia.activada, que los necesita para el correo;
	// el trigger los guarda en todos los eventos y acá se quitan (minimización).
	rows, err := tx.Query(ctx, `SELECT event_id::text,event_type,event_version,occurred_at,
		CASE WHEN event_type='membresia.activada' THEN data ELSE data - 'alumno_email' - 'alumno_nombre' END FROM `+tabla+` WHERE publicado_en IS NULL AND event_type IN (`+tipos+`) ORDER BY occurred_at,event_id LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type event struct {
		ID          string          `json:"event_id"`
		Type        string          `json:"event_type"`
		Version     int             `json:"event_version"`
		Occurred    time.Time       `json:"occurred_at"`
		Producer    string          `json:"producer"`
		Correlation string          `json:"correlation_id"`
		Traceparent string          `json:"traceparent"`
		Data        json.RawMessage `json:"data"`
	}
	var events []event
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.ID, &e.Type, &e.Version, &e.Occurred, &e.Data); err != nil {
			rows.Close()
			return 0, err
		}
		events = append(events, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, e := range events {
		e.Producer = "members-service"
		body, err := json.Marshal(e)
		if err != nil {
			return 0, err
		}
		publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = bus.Publish(publishCtx, "gym.events", e.Type, amqp.Publishing{ContentType: "application/json", MessageId: e.ID, Type: e.Type, Timestamp: e.Occurred, Body: body})
		cancel()
		if err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `UPDATE `+tabla+` SET publicado_en=now() WHERE event_id=$1`, e.ID); err != nil {
			return 0, err
		}
	}
	return len(events), tx.Commit(ctx)
}
