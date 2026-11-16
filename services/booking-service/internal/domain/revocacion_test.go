// RN-04, RN-34, CL-12 y CL-19 (D-98): qué reservas cancela una baja informada
// por members-service.
package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func reservaEn(id string, inicio time.Time, estado string) Reserva {
	return Reserva{ID: id, Estado: estado, Clase: Clase{Inicio: inicio, Fin: inicio.Add(time.Hour)}}
}

func TestRN04MembresiaCanceladaConservaFechasCubiertas(t *testing.T) {
	lunes := time.Date(2026, 11, 16, 10, 0, 0, 0, Zona)
	martes := lunes.AddDate(0, 0, 1)
	reservas := []Reserva{reservaEn("a", lunes, ReservaConfirmada), reservaEn("b", martes, ReservaConfirmada), reservaEn("c", martes, ReservaCancelada)}
	r := Revocacion{Tipo: RevocacionMembresiaCancelada}

	// CL-12: otra membresía vigente cubre el lunes; el martes queda sin cobertura.
	got := r.ReservasACancelar(reservas, Vigencia{AlumnoActivo: true, VigenteEn: map[string]bool{"2026-11-16": true}})
	if !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("CL-12: solo se cancela la reserva sin cobertura y confirmada: %v", got)
	}
	if got := r.ReservasACancelar(reservas, Vigencia{AlumnoActivo: false, VigenteEn: map[string]bool{"2026-11-16": true}}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("un alumno inactivo no conserva reservas aunque tenga cobertura: %v", got)
	}
}

func TestRN34BajaCancelaTodasLasConfirmadas(t *testing.T) {
	lunes := time.Date(2026, 11, 16, 10, 0, 0, 0, Zona)
	reservas := []Reserva{reservaEn("a", lunes, ReservaConfirmada), reservaEn("b", lunes.Add(3*time.Hour), ReservaAsistida)}
	r := Revocacion{Tipo: RevocacionUsuarioDesactivado}
	if got := r.ReservasACancelar(reservas, Vigencia{AlumnoActivo: true, VigenteEn: map[string]bool{"2026-11-16": true}}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("RN-34: la baja cancela las confirmadas aunque haya cobertura: %v", got)
	}
	if r.Motivo() != MotivoAlumnoDadoDeBaja || !errors.Is(r.Error(), ErrUsuarioInactivo) {
		t.Error("la baja se informa como alumno dado de baja / usuario inactivo")
	}
	m := Revocacion{Tipo: RevocacionMembresiaCancelada}
	if m.Motivo() != MotivoMembresiaCancelada || !errors.Is(m.Error(), ErrMembresiaNoVigente) {
		t.Error("CL-19: la reserva que compite con la cancelación se rechaza como membresía no vigente")
	}
}

func TestFechasDeReservasSinRepetir(t *testing.T) {
	lunes := time.Date(2026, 11, 16, 23, 30, 0, 0, Zona) // 02:30 UTC del martes: cuenta la fecha argentina
	reservas := []Reserva{reservaEn("a", lunes.AddDate(0, 0, 1), ReservaConfirmada), reservaEn("b", lunes, ReservaConfirmada), reservaEn("c", lunes, ReservaConfirmada)}
	if got := FechasDeReservas(reservas); !reflect.DeepEqual(got, []string{"2026-11-16", "2026-11-17"}) {
		t.Errorf("fechas distintas, ordenadas y en zona de Argentina: %v", got)
	}
}
