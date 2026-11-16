// RN-04, RN-34, CL-12 y D-98: el caso de uso Revocar con dobles en memoria. La
// concurrencia con Reservar (CL-19) se prueba contra PostgreSQL.
package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/ports"
)

const (
	eventoID    = "00000000-0000-4000-8000-000000000030"
	membresiaID = "00000000-0000-4000-8000-000000000031"
)

type revocaciones struct {
	reservas
	futuras   []domain.Reserva
	elegidas  []string
	repetido  bool
	recibidas []domain.Revocacion
}

func (r *revocaciones) Revocar(_ context.Context, rev domain.Revocacion, _ time.Time, elegir func([]domain.Reserva) ([]string, error)) (int, bool, error) {
	r.recibidas = append(r.recibidas, rev)
	if r.repetido {
		return 0, true, nil
	}
	ids, err := elegir(r.futuras)
	r.elegidas = ids
	return len(ids), false, err
}

type membersContador struct {
	members
	llamadas int
	fechas   [][]string
}

func (m *membersContador) Vigencia(_ context.Context, _ string, fechas []string) (domain.Vigencia, error) {
	m.llamadas++
	m.fechas = append(m.fechas, fechas)
	return m.vigencia, m.err
}

func escenarioRevocacion(t *testing.T) (*Servicio, *revocaciones, *membersContador, time.Time) {
	t.Helper()
	ahora := time.Date(2026, 11, 10, 15, 0, 0, 0, domain.Zona)
	res := &revocaciones{}
	for i := 0; i < 3; i++ {
		inicio := ahora.AddDate(0, 0, i+1)
		res.futuras = append(res.futuras, domain.Reserva{ID: string(rune('a' + i)), AlumnoID: alumno, Estado: domain.ReservaConfirmada, Clase: domain.Clase{Inicio: inicio, Fin: inicio.Add(time.Hour)}})
	}
	m := &membersContador{members: members{vigencia: domain.Vigencia{AlumnoActivo: true, VigenteHoy: true, VigenteEn: map[string]bool{"2026-11-11": true}}}}
	return New(&catalogo{}, res, m, reloj{ahora}), res, m, ahora
}

func TestRN04RevocarMembresiaConsultaCobertura(t *testing.T) {
	s, res, m, ahora := escenarioRevocacion(t)
	n, err := s.Revocar(context.Background(), domain.Revocacion{EventoID: eventoID, Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, MembresiaID: membresiaID, OcurridaEn: ahora})
	if err != nil || n != 2 || !reflect.DeepEqual(res.elegidas, []string{"b", "c"}) {
		t.Fatalf("CL-12: se cancelan las reservas del 12 y 13; la del 11 sigue cubierta: n=%d %v %v", n, res.elegidas, err)
	}
	if m.llamadas != 1 || !reflect.DeepEqual(m.fechas[0], []string{"2026-11-11", "2026-11-12", "2026-11-13"}) {
		t.Errorf("una consulta a members con las fechas de las reservas: %v", m.fechas)
	}
}

func TestRN04MembersCaidoReintenta(t *testing.T) {
	s, _, m, ahora := escenarioRevocacion(t)
	m.err = errors.New("members caído")
	_, err := s.Revocar(context.Background(), domain.Revocacion{EventoID: eventoID, Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, MembresiaID: membresiaID, OcurridaEn: ahora})
	if !errors.Is(err, ports.ErrNoDisponible) {
		t.Errorf("sin members no se decide: el evento vuelve a la cola de reintentos: %v", err)
	}
}

func TestRN34RevocarBajaSinConsultarMembers(t *testing.T) {
	s, res, m, ahora := escenarioRevocacion(t)
	n, err := s.Revocar(context.Background(), domain.Revocacion{EventoID: eventoID, Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: alumno, OcurridaEn: ahora})
	if err != nil || n != 3 || m.llamadas != 0 {
		t.Errorf("RN-34: la baja cancela todas las reservas futuras sin consultar cobertura: n=%d llamadas=%d %v", n, m.llamadas, err)
	}
	res.repetido = true
	if n, err = s.Revocar(context.Background(), domain.Revocacion{EventoID: eventoID, Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: alumno, OcurridaEn: ahora}); err != nil || n != 0 {
		t.Errorf("regla 7: una reentrega no tiene efecto: n=%d %v", n, err)
	}
}

func TestRevocarMuchasFechasEnLotes(t *testing.T) {
	s, res, m, ahora := escenarioRevocacion(t)
	res.futuras = nil
	for i := 0; i < 40; i++ {
		inicio := ahora.AddDate(0, 0, i+1)
		res.futuras = append(res.futuras, domain.Reserva{ID: inicio.Format(time.DateOnly), Estado: domain.ReservaConfirmada, Clase: domain.Clase{Inicio: inicio, Fin: inicio.Add(time.Hour)}})
	}
	if _, err := s.Revocar(context.Background(), domain.Revocacion{EventoID: eventoID, Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, MembresiaID: membresiaID, OcurridaEn: ahora}); err != nil {
		t.Fatal(err)
	}
	if m.llamadas != 2 || len(m.fechas[0]) != 31 || len(m.fechas[1]) != 9 {
		t.Errorf("members acepta hasta 31 fechas por consulta: %d llamadas", m.llamadas)
	}
}

func TestRevocarValidaElEvento(t *testing.T) {
	s, res, _, ahora := escenarioRevocacion(t)
	invalidos := []domain.Revocacion{
		{EventoID: "x", Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: alumno, OcurridaEn: ahora},
		{EventoID: eventoID, Tipo: domain.RevocacionMembresiaCancelada, AlumnoID: alumno, OcurridaEn: ahora},
		{EventoID: eventoID, Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: alumno, MembresiaID: membresiaID, OcurridaEn: ahora},
		{EventoID: eventoID, Tipo: "OTRA", AlumnoID: alumno, OcurridaEn: ahora},
		{EventoID: eventoID, Tipo: domain.RevocacionUsuarioDesactivado, AlumnoID: alumno},
	}
	for i, r := range invalidos {
		if _, err := s.Revocar(context.Background(), r); !errors.Is(err, domain.ErrValidacion) {
			t.Errorf("caso %d: %v", i, err)
		}
	}
	if len(res.recibidas) != 0 {
		t.Error("un evento inválido no llega a la base")
	}
}
