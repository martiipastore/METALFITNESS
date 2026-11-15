// Balanceo de la etapa 12, parte B (enunciado §2.3, D-46, D-51, ADR-012 v3).
// Casos de "Cómo se verificará" de ADR-012 v2.
package balance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func nuevo(t *testing.T, n int) *Balanceador {
	t.Helper()
	urls := make([]*url.URL, n)
	for i := range urls {
		urls[i], _ = url.Parse("http://booking-" + string(rune('a'+i)) + ":8082")
	}
	return New("booking-service", urls, PoliticaAprobada, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRetiroTrasTresFallosConsecutivos(t *testing.T) {
	b := nuevo(t, 2)
	i := b.Instancias[0]
	b.Registrar(i, false)
	b.Registrar(i, false)
	if !i.Elegible() {
		t.Fatal("uno y dos fallos no retiran la instancia")
	}
	b.Registrar(i, false)
	if i.Elegible() || i.Retiros.Load() != 1 {
		t.Fatal("el tercer fallo consecutivo la retira")
	}
}

func TestExitoIntermedioReiniciaLaCuentaDeFallos(t *testing.T) {
	b := nuevo(t, 2)
	i := b.Instancias[0]
	b.Registrar(i, false)
	b.Registrar(i, false)
	b.Registrar(i, true)
	b.Registrar(i, false)
	b.Registrar(i, false)
	if !i.Elegible() {
		t.Fatal("un éxito reinicia la cuenta: dos fallos más no alcanzan")
	}
	b.Registrar(i, false)
	if i.Elegible() {
		t.Fatal("tres fallos seguidos después del éxito la retiran")
	}
}

func TestReincorporacionTrasDosExitosConsecutivos(t *testing.T) {
	b := nuevo(t, 2)
	i := b.Instancias[0]
	for k := 0; k < 3; k++ {
		b.Registrar(i, false)
	}
	b.Registrar(i, true)
	if i.Elegible() {
		t.Fatal("un solo éxito no la reincorpora")
	}
	b.Registrar(i, false)
	b.Registrar(i, true)
	if i.Elegible() {
		t.Fatal("un fallo intermedio reinicia la cuenta de éxitos")
	}
	b.Registrar(i, true)
	if !i.Elegible() || i.Reingresos.Load() != 1 {
		t.Fatal("dos éxitos consecutivos la reincorporan")
	}
}

func TestRoundRobinEquitativoYSinInstancias(t *testing.T) {
	b := nuevo(t, 2)
	conteo := map[string]int{}
	for k := 0; k < 100; k++ {
		i, err := b.Elegir()
		if err != nil {
			t.Fatal(err)
		}
		conteo[i.Nombre]++
	}
	if conteo["booking-service-1"] != 50 || conteo["booking-service-2"] != 50 {
		t.Fatalf("igual peso: %v", conteo)
	}
	for k := 0; k < 3; k++ {
		b.Registrar(b.Instancias[0], false)
	}
	for k := 0; k < 10; k++ {
		if i, _ := b.Elegir(); i.Nombre != "booking-service-2" {
			t.Fatal("una instancia retirada no recibe tráfico")
		}
	}
	for k := 0; k < 3; k++ {
		b.Registrar(b.Instancias[1], false)
	}
	if _, err := b.Elegir(); !errors.Is(err, ErrSinInstancias) {
		t.Fatalf("sin instancias elegibles: %v", err)
	}
}

func TestChequeoActivoContraServidoresReales(t *testing.T) {
	var lenta atomic.Bool
	sana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			t.Errorf("ruta chequeada: %s", r.URL.Path)
		}
		if lenta.Load() {
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sana.Close()
	caida := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer caida.Close()
	u1, _ := url.Parse(sana.URL)
	u2, _ := url.Parse(caida.URL)
	politica := Politica{Intervalo: time.Hour, Timeout: 100 * time.Millisecond, FallosParaRetirar: 3, ExitosParaReincorporar: 2}
	b := New("booking-service", []*url.URL{u1, u2}, politica, sana.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	for k := 0; k < 3; k++ {
		b.ChequearTodas(ctx)
	}
	if !b.Instancias[0].Elegible() || b.Instancias[1].Elegible() {
		t.Fatal("200 mantiene la instancia; 503 tres veces la retira")
	}
	lenta.Store(true)
	for k := 0; k < 3; k++ {
		b.ChequearTodas(ctx)
	}
	if b.Instancias[0].Elegible() {
		t.Fatal("una respuesta más lenta que el timeout cuenta como fallo")
	}
}
