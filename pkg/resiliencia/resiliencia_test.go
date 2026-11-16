// ADR-005 v2 y D-102: timeout por intento, un reintento solo en lecturas y
// circuit breaker 5 fallas / 10 s / una prueba.
package resiliencia

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type reloj struct{ t time.Time }

func breakerDePrueba(r *reloj) *Breaker {
	b := NuevoBreaker(5, 10*time.Second)
	b.now = func() time.Time { return r.t }
	return b
}

func TestBreakerSeAbreTrasCincoFallasYPruebaUnaVez(t *testing.T) {
	r := &reloj{t: time.Unix(0, 0)}
	b := breakerDePrueba(r)
	var cambios []string
	b.AlCambiar = func(desde, hacia string) { cambios = append(cambios, desde+"->"+hacia) }
	for i := 0; i < 4; i++ {
		if b.Permitir() != nil {
			t.Fatal("cerrado deja pasar")
		}
		b.Registrar(false)
	}
	if b.Estado() != Cerrado {
		t.Fatal("cuatro fallas no abren")
	}
	b.Permitir()
	b.Registrar(true) // un éxito reinicia la cuenta
	for i := 0; i < 5; i++ {
		b.Permitir()
		b.Registrar(false)
	}
	if !errors.Is(b.Permitir(), ErrCircuitoAbierto) || b.Estado() != Abierto {
		t.Fatal("cinco fallas seguidas abren el circuito y rechazan enseguida")
	}
	r.t = r.t.Add(10 * time.Second)
	if b.Permitir() != nil {
		t.Fatal("pasada la apertura, deja salir una prueba")
	}
	if !errors.Is(b.Permitir(), ErrCircuitoAbierto) {
		t.Fatal("en medio abierto, una sola prueba a la vez")
	}
	b.Registrar(false)
	if b.Estado() != Abierto {
		t.Fatal("una prueba fallida vuelve a abrir")
	}
	r.t = r.t.Add(10 * time.Second)
	b.Permitir()
	b.Registrar(true)
	if b.Estado() != Cerrado || b.Permitir() != nil {
		t.Fatal("una prueba exitosa cierra")
	}
	esperado := []string{"cerrado->abierto", "abierto->medio_abierto", "medio_abierto->abierto", "abierto->medio_abierto", "medio_abierto->cerrado"}
	if len(cambios) != len(esperado) {
		t.Fatalf("transiciones: %v", cambios)
	}
	for i := range esperado {
		if cambios[i] != esperado[i] {
			t.Fatalf("transiciones: %v", cambios)
		}
	}
}

func servidor(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s, &n
}

func cliente(b *Breaker) *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: Transport{Breaker: b, PorIntento: 200 * time.Millisecond, Reintentos: 1, Espera: 10 * time.Millisecond}}
}

func TestReintentaUnaVezSoloLecturas(t *testing.T) {
	var fallar atomic.Bool
	fallar.Store(true)
	s, n := servidor(t, func(w http.ResponseWriter, _ *http.Request) {
		if fallar.Swap(false) {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	res, err := cliente(nil).Get(s.URL)
	if err != nil || res.StatusCode != 200 || n.Load() != 2 {
		t.Fatalf("un 503 transitorio se reintenta una vez: %v %d", err, n.Load())
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(body) != `{"ok":true}` {
		t.Fatal("el cuerpo se lee después de devolver la respuesta")
	}

	fallar.Store(true)
	n.Store(0)
	res, err = cliente(nil).Post(s.URL, "application/json", nil)
	if err != nil || res.StatusCode != 503 || n.Load() != 1 {
		t.Fatalf("una escritura nunca se reintenta: %v %d", err, n.Load())
	}
	_ = res.Body.Close()
}

func TestTimeoutPorIntentoYFallaFinal(t *testing.T) {
	s, n := servidor(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	inicio := time.Now()
	_, err := cliente(nil).Get(s.URL)
	if err == nil || n.Load() != 2 || time.Since(inicio) > 900*time.Millisecond {
		t.Fatalf("dos intentos de 200 ms, no la espera completa: %v %d %s", err, n.Load(), time.Since(inicio))
	}
}

func TestCircuitoAbiertoNoLlegaAlDestino(t *testing.T) {
	s, n := servidor(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	b := NuevoBreaker(5, time.Hour)
	c := cliente(b)
	for i := 0; i < 3; i++ { // 3 llamadas × 2 intentos = 6 fallas
		if res, err := c.Get(s.URL); err == nil {
			_ = res.Body.Close()
		}
	}
	if b.Estado() != Abierto {
		t.Fatalf("estado %s tras seis fallas", b.Estado())
	}
	antes := n.Load()
	_, err := c.Get(s.URL)
	if !errors.Is(err, ErrCircuitoAbierto) || n.Load() != antes {
		t.Fatalf("abierto: rechaza sin llamar al destino: %v", err)
	}
}

func TestUn4xxNoCuentaComoFalla(t *testing.T) {
	s, _ := servidor(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	b := NuevoBreaker(1, time.Hour)
	res, err := cliente(b).Get(s.URL)
	if err != nil || res.StatusCode != 404 || b.Estado() != Cerrado {
		t.Fatal("un 404 es una respuesta del destino, no una falla")
	}
	_ = res.Body.Close()
}

func TestCancelacionDelLlamadorNoSeReintenta(t *testing.T) {
	s, n := servidor(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if _, err := cliente(nil).Do(req); err == nil || n.Load() > 1 {
		t.Fatalf("con la solicitud cancelada no hay reintento: %v %d", err, n.Load())
	}
}
