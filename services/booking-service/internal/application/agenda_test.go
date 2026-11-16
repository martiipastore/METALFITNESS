// RN-34, CA-02.3 y D-99: la agenda que members consulta antes de dar de baja a
// un profesor, y la generación que no crea clases de profesores inactivos.
package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/ports"
)

const otroProfesor = "00000000-0000-4000-8000-000000000005"

type catalogoAgenda struct {
	catalogo
	clases    []domain.Clase
	horarios  []domain.Horario
	guardadas []domain.Clase
}

func (c *catalogoAgenda) ClasesDeProfesor(context.Context, string, time.Time) ([]domain.Clase, error) {
	return c.clases, nil
}
func (c *catalogoAgenda) Actividades(context.Context) ([]domain.Actividad, error) {
	return []domain.Actividad{c.actividad}, nil
}
func (c *catalogoAgenda) Horarios(context.Context, string) ([]domain.Horario, error) {
	return c.horarios, nil
}
func (c *catalogoAgenda) GuardarClases(_ context.Context, _ string, clases []domain.Clase) (int, error) {
	c.guardadas = clases
	return len(clases), nil
}

func TestCA02_3AgendaCuentaSoloClasesFuturasProgramadas(t *testing.T) {
	ahora := time.Date(2026, 11, 16, 10, 0, 0, 0, domain.Zona)
	cat := &catalogoAgenda{clases: []domain.Clase{
		{Inicio: ahora.Add(time.Hour), Estado: domain.ClaseProgramada},
		{Inicio: ahora.Add(48 * time.Hour), Estado: domain.ClaseProgramada},
		{Inicio: ahora.Add(24 * time.Hour), Estado: domain.ClaseCancelada},
		{Inicio: ahora.Add(-time.Minute), Estado: domain.ClaseProgramada}, // ya empezó
	}}
	s := New(cat, &reservas{}, &members{}, reloj{ahora})
	n, err := s.AgendaDeProfesor(context.Background(), profesor)
	if err != nil || n != 2 {
		t.Errorf("CA-02.3: dos clases futuras PROGRAMADA: n=%d %v", n, err)
	}
	if _, err = s.AgendaDeProfesor(context.Background(), "no-es-uuid"); !errors.Is(err, domain.ErrValidacion) {
		t.Error("el identificador del profesor se valida")
	}
}

func TestRN34GeneracionSalteaProfesoresInactivos(t *testing.T) {
	ahora := time.Date(2026, 11, 16, 10, 0, 0, 0, domain.Zona)
	cat := &catalogoAgenda{
		catalogo: catalogo{actividad: domain.Actividad{ID: actID, Nombre: "Zumba", CapacidadMaxima: 30, Activa: true}},
		horarios: []domain.Horario{
			{ActividadID: actID, DiaSemana: 2, InicioMinutos: 600, DuracionMinutos: 60, ProfesorID: profesor, Capacidad: 20},
			{ActividadID: actID, DiaSemana: 3, InicioMinutos: 600, DuracionMinutos: 60, ProfesorID: otroProfesor, Capacidad: 20},
		},
	}
	m := &members{profesores: map[string]domain.Persona{
		profesor:     {ID: profesor, Estado: "ACTIVO"},
		otroProfesor: {ID: otroProfesor, Estado: "INACTIVO"},
	}}
	s := New(cat, &reservas{}, m, reloj{ahora})
	n, err := s.GenerarClases(context.Background(), Actor{ID: admin, Rol: RolAdministrador}, "2026-11-17", "2026-11-18")
	if err != nil || n != 1 || len(cat.guardadas) != 1 || cat.guardadas[0].ProfesorID != profesor {
		t.Fatalf("RN-34: solo se genera la clase del profesor activo: n=%d %v", n, err)
	}

	m.err = errors.New("members caído")
	if _, err = s.GenerarClases(context.Background(), Actor{ID: admin, Rol: RolAdministrador}, "2026-11-17", "2026-11-18"); !errors.Is(err, ports.ErrNoDisponible) {
		t.Errorf("sin members no se generan clases de profesores sin confirmar: %v", err)
	}
}
