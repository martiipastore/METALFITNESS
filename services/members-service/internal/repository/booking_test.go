// D-99: cliente de la agenda de profesores en booking (CA-02.3).
package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBookingHTTPUsaLaPrimeraInstanciaQueResponde(t *testing.T) {
	var clave string
	sana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clave = r.Header.Get("X-Internal-Service-Key")
		if r.URL.Path != "/internal/v1/profesores/00000000-0000-4000-8000-000000000002/agenda" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"clases_programadas":2}`))
	}))
	defer sana.Close()
	caida := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer caida.Close()

	b := BookingHTTP{URLs: []string{caida.URL, " " + sana.URL + "/ "}, Secret: "clave-interna", Client: &http.Client{Timeout: time.Second}}
	n, err := b.FutureClasses(context.Background(), "00000000-0000-4000-8000-000000000002")
	if err != nil || n != 2 || clave != "clave-interna" {
		t.Fatalf("si una instancia falla, responde la otra con la clave interna: n=%d %v", n, err)
	}
}

func TestBookingHTTPFallaSiNingunaRespondeBien(t *testing.T) {
	invalida := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"otra_cosa":1}`))
	}))
	defer invalida.Close()
	for _, urls := range [][]string{nil, {invalida.URL}, {"http://127.0.0.1:1"}} {
		b := BookingHTTP{URLs: urls, Secret: "x", Client: &http.Client{Timeout: time.Second}}
		if _, err := b.FutureClasses(context.Background(), "00000000-0000-4000-8000-000000000002"); err == nil {
			t.Errorf("%v: sin una respuesta válida no se supone que no hay clases", urls)
		}
	}
}
