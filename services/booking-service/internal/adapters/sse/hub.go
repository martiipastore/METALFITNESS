// Package sse difunde a los navegadores conectados los cambios de ocupación de
// las clases (bonus D-40, D-103). Es un adaptador de presentación: no decide
// nada del negocio, solo reenvía lo que publicó clase.actualizada. La reserva
// sigue validándose en PostgreSQL; esto solo mantiene la pantalla al día.
package sse

import (
	"sync"
	"sync/atomic"
)

// Cambio es lo que recibe el navegador: sin datos de personas, solo el estado
// público de la clase que cualquier usuario autenticado ya ve en la búsqueda.
type Cambio struct {
	ClaseID   string `json:"clase_id"`
	Ocupacion int    `json:"ocupacion"`
	Capacidad int    `json:"capacidad"`
	Estado    string `json:"estado"`
	Version   int64  `json:"version"`
}

// Hub reparte cada cambio a todas las suscripciones de esta instancia. Un
// suscriptor lento pierde mensajes en lugar de frenar a los demás: su pantalla
// se resincroniza al reconectarse y con la recarga periódica.
type Hub struct {
	Maximo int // conexiones simultáneas por instancia

	mu                    sync.Mutex
	subs                  map[chan Cambio]struct{}
	enviados, descartados atomic.Int64
}

// NuevoHub crea un difusor con un límite de conexiones.
func NuevoHub(maximo int) *Hub {
	return &Hub{Maximo: maximo, subs: map[chan Cambio]struct{}{}}
}

// Suscribir devuelve un canal de cambios y la función para darse de baja. ok es
// false si la instancia ya tiene el máximo de conexiones.
func (h *Hub) Suscribir() (<-chan Cambio, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= h.Maximo {
		return nil, func() {}, false
	}
	c := make(chan Cambio, 32)
	h.subs[c] = struct{}{}
	var una sync.Once
	return c, func() {
		una.Do(func() {
			h.mu.Lock()
			delete(h.subs, c)
			h.mu.Unlock()
		})
	}, true
}

// Publicar entrega el cambio sin bloquear.
func (h *Hub) Publicar(c Cambio) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s <- c:
			h.enviados.Add(1)
		default:
			h.descartados.Add(1)
		}
	}
}

// Conectados devuelve cuántas suscripciones hay abiertas.
func (h *Hub) Conectados() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Contadores devuelve los mensajes enviados y los descartados por lentitud.
func (h *Hub) Contadores() (enviados, descartados int64) {
	return h.enviados.Load(), h.descartados.Load()
}
