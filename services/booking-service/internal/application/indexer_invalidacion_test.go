// Doble invalidación del indexer (ADR-007 v2): la caché de búsquedas se invalida
// al aplicar un cambio y otra vez cuando OpenSearch ya lo hizo visible.
package application

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
)

type indiceAplica struct{ aplicado bool }

func (i indiceAplica) Indexar(context.Context, domain.Clase) (bool, error) { return i.aplicado, nil }

type invalidacionContada struct{ n atomic.Int64 }

func (c *invalidacionContada) InvalidarClases(context.Context) error {
	c.n.Add(1)
	return nil
}

func eventoValido(version int64) domain.EventoClase {
	inicio := time.Date(2026, 11, 20, 19, 0, 0, 0, domain.Zona)
	return domain.EventoClase{OcurridoEn: inicio.Add(-time.Hour), Clase: domain.Clase{ID: claseID, ActividadID: actID, ActividadNombre: "Yoga", ProfesorID: profesor,
		Inicio: inicio, Fin: inicio.Add(time.Hour), Capacidad: 12, Estado: domain.ClaseProgramada, Version: version}}
}

func TestIndexadorInvalidaDosVeces(t *testing.T) {
	ctx := context.Background()
	cache := &invalidacionContada{}
	espera := 80 * time.Millisecond
	ix := NewIndexador(indiceAplica{aplicado: true}, reloj{time.Now()}).ConInvalidacion(cache, espera)

	for v := int64(1); v <= 3; v++ {
		if _, err := ix.Aplicar(ctx, eventoValido(v)); err != nil {
			t.Fatal(err)
		}
	}
	if got := cache.n.Load(); got != 3 {
		t.Fatalf("una invalidación inmediata por versión aplicada: %d", got)
	}
	time.Sleep(espera * 3)
	if got := cache.n.Load(); got != 4 {
		t.Fatalf("una sola invalidación diferida tras la última versión: %d", got)
	}

	descartado := &invalidacionContada{}
	viejo := NewIndexador(indiceAplica{aplicado: false}, reloj{time.Now()}).ConInvalidacion(descartado, espera)
	if _, err := viejo.Aplicar(ctx, eventoValido(1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(espera * 2)
	if descartado.n.Load() != 0 {
		t.Fatal("un evento viejo no cambia el índice ni invalida la caché")
	}
}
