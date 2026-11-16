// RN-34 y CA-02.3 (D-99): un profesor con clases futuras PROGRAMADA no se da de
// baja; si booking no responde, la baja falla cerrada.
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/access"
	"github.com/GuadiViti/Arquitectura-de-Software/services/members-service/internal/model"
)

const (
	adminID   = "00000000-0000-4000-8000-000000000001"
	teacherID = "00000000-0000-4000-8000-000000000002"
)

type peopleRepoFalso struct {
	PeopleRepository
	person model.Person
	bajas  int
}

func (r *peopleRepoFalso) Person(context.Context, string) (model.Person, error) {
	return r.person, nil
}
func (r *peopleRepoFalso) DeactivatePerson(_ context.Context, _, _, _ string, check func(model.Person) error, _ time.Time) (model.Person, error) {
	if err := check(r.person); err != nil {
		return model.Person{}, err
	}
	r.bajas++
	return r.person, nil
}

type agendaFalsa struct {
	n     int
	err   error
	vista int
}

func (a *agendaFalsa) FutureClasses(context.Context, string) (int, error) {
	a.vista++
	return a.n, a.err
}

func personaActiva(role string) model.Person {
	dni := "30111222"
	return model.Person{ID: teacherID, Role: role, DNI: &dni, State: "ACTIVO"}
}

func TestCA02_3BajaDeProfesorConClasesFuturas(t *testing.T) {
	repo := &peopleRepoFalso{person: personaActiva(access.Teacher)}
	agenda := &agendaFalsa{n: 3}
	s := (&People{repo: repo, now: time.Now}).WithAgenda(agenda)

	_, err := s.Deactivate(context.Background(), adminID, access.Admin, teacherID, "Deja el gimnasio")
	if !errors.Is(err, model.ErrTeacherHasClasses) || repo.bajas != 0 {
		t.Fatalf("CA-02.3: con 3 clases programadas se rechaza: %v", err)
	}
	agenda.n = 0
	if _, err = s.Deactivate(context.Background(), adminID, access.Admin, teacherID, "Deja el gimnasio"); err != nil || repo.bajas != 1 {
		t.Errorf("sin clases futuras se da de baja: %v", err)
	}
}

func TestCA02_3BookingCaidoFallaCerrado(t *testing.T) {
	repo := &peopleRepoFalso{person: personaActiva(access.Teacher)}
	s := (&People{repo: repo, now: time.Now}).WithAgenda(&agendaFalsa{err: errors.New("booking no disponible")})
	_, err := s.Deactivate(context.Background(), adminID, access.Admin, teacherID, "Deja el gimnasio")
	if err == nil || errors.Is(err, model.ErrTeacherHasClasses) || repo.bajas != 0 {
		t.Errorf("sin respuesta de booking no se da de baja (503 en el handler): %v", err)
	}
}

func TestCA02_3SoloSeConsultaParaProfesores(t *testing.T) {
	repo := &peopleRepoFalso{person: personaActiva(access.Student)}
	agenda := &agendaFalsa{n: 5}
	s := (&People{repo: repo, now: time.Now}).WithAgenda(agenda)
	if _, err := s.Deactivate(context.Background(), adminID, access.Admin, teacherID, "Baja pedida"); err != nil || agenda.vista != 0 || repo.bajas != 1 {
		t.Errorf("un alumno se da de baja sin consultar la agenda (sus reservas las cancela booking): %v", err)
	}
	sinAgenda := &People{repo: &peopleRepoFalso{person: personaActiva(access.Teacher)}, now: time.Now}
	if _, err := sinAgenda.Deactivate(context.Background(), adminID, access.Admin, teacherID, "Baja pedida"); err != nil {
		t.Errorf("sin booking configurado (Vercel) la baja no se bloquea: %v", err)
	}
}
