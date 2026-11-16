// Package resiliencia protege las llamadas HTTP entre servicios con timeout por
// intento, un reintento acotado y un circuit breaker (ADR-005 v2, D-102;
// teórico, clase 7). Es técnico: no conoce reglas de negocio ni rutas.
package resiliencia

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrCircuitoAbierto indica que el destino falló varias veces seguidas y la
// llamada se rechaza sin esperar. Quien llama lo trata como dependencia no
// disponible (503, falla cerrada).
var ErrCircuitoAbierto = errors.New("circuito abierto: dependencia no disponible")

// Estados del circuit breaker.
const (
	Cerrado      = "cerrado"
	Abierto      = "abierto"
	MedioAbierto = "medio_abierto"
)

// Breaker abre el circuito tras Umbral fallas consecutivas, rechaza durante
// Apertura y después deja pasar una sola prueba: si sale bien se cierra, si
// falla vuelve a abrirse.
type Breaker struct {
	Umbral   int
	Apertura time.Duration
	// AlCambiar se llama en cada transición (para logs y métricas).
	AlCambiar func(desde, hacia string)
	now       func() time.Time

	mu           sync.Mutex
	estado       string
	fallas       int
	abiertoHasta time.Time
	probando     bool
}

// NuevoBreaker crea un circuit breaker cerrado.
func NuevoBreaker(umbral int, apertura time.Duration) *Breaker {
	return &Breaker{Umbral: umbral, Apertura: apertura, estado: Cerrado, now: time.Now}
}

// Estado devuelve el estado actual.
func (b *Breaker) Estado() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.estado == Abierto && !b.now().Before(b.abiertoHasta) {
		return MedioAbierto
	}
	return b.estado
}

// Permitir decide si la llamada puede salir. En medio abierto deja pasar solo
// una prueba a la vez.
func (b *Breaker) Permitir() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.estado {
	case Abierto:
		if b.now().Before(b.abiertoHasta) {
			return ErrCircuitoAbierto
		}
		b.cambiar(MedioAbierto)
		fallthrough
	case MedioAbierto:
		if b.probando {
			return ErrCircuitoAbierto
		}
		b.probando = true
	}
	return nil
}

// Registrar informa el resultado de una llamada permitida.
func (b *Breaker) Registrar(exito bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probando = false
	if exito {
		b.fallas = 0
		if b.estado != Cerrado {
			b.cambiar(Cerrado)
		}
		return
	}
	b.fallas++
	if b.estado == MedioAbierto || b.fallas >= b.Umbral {
		b.abiertoHasta = b.now().Add(b.Apertura)
		if b.estado != Abierto {
			b.cambiar(Abierto)
		}
	}
}

func (b *Breaker) cambiar(hacia string) {
	desde := b.estado
	b.estado = hacia
	if b.AlCambiar != nil && desde != hacia {
		b.AlCambiar(desde, hacia)
	}
}

// Transport aplica, en este orden, el circuit breaker, el timeout por intento y
// un reintento. Solo reintenta GET y HEAD (idempotentes) ante un error de red,
// un timeout o una respuesta 502, 503 o 504; nunca una escritura (ADR-005 v2).
// Un 4xx es una respuesta válida del destino y no cuenta como falla.
type Transport struct {
	Base       http.RoundTripper
	Breaker    *Breaker
	PorIntento time.Duration // 800 ms según ADR-005 v2
	Reintentos int           // 1 según ADR-005 v2
	Espera     time.Duration // 100 ms según ADR-005 v2
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	reintentable := r.Method == http.MethodGet || r.Method == http.MethodHead
	intentos := 1
	if reintentable {
		intentos += t.Reintentos
	}
	var ultimo error
	for i := 0; i < intentos; i++ {
		if i > 0 {
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-time.After(t.Espera):
			}
		}
		if t.Breaker != nil {
			if err := t.Breaker.Permitir(); err != nil {
				return nil, err
			}
		}
		res, err := t.intento(base, r)
		falla := err != nil || res.StatusCode == http.StatusBadGateway || res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusGatewayTimeout
		if t.Breaker != nil {
			t.Breaker.Registrar(!falla)
		}
		if !falla || i == intentos-1 || r.Context().Err() != nil {
			return res, err
		}
		if res != nil {
			_ = res.Body.Close()
		}
		ultimo = err
	}
	return nil, ultimo
}

// intento hace una solicitud con su propio timeout. El contexto se cancela al
// cerrar el cuerpo de la respuesta, no antes: quien llama todavía lo lee.
func (t Transport) intento(base http.RoundTripper, r *http.Request) (*http.Response, error) {
	if t.PorIntento <= 0 {
		return base.RoundTrip(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), t.PorIntento)
	res, err := base.RoundTrip(r.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	res.Body = cuerpo{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

type cuerpo struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cuerpo) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
