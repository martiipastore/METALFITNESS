package domain

import (
	"sort"
	"time"
)

// Tipos de revocación que llegan desde members-service (D-20, D-98).
const (
	RevocacionMembresiaCancelada = "MEMBRESIA_CANCELADA" // membresia.cancelada (RN-04)
	RevocacionUsuarioDesactivado = "USUARIO_DESACTIVADO" // usuario.desactivado (RN-34)
)

// Motivos de las cancelaciones que hace el sistema por una revocación.
const (
	MotivoMembresiaCancelada = "membresía cancelada"
	MotivoAlumnoDadoDeBaja   = "alumno dado de baja"
)

// VentanaRevocacion cubre la diferencia de reloj entre el proceso de Booking que
// validó la vigencia y PostgreSQL, que fecha la revocación. Una reserva validada
// hasta este tiempo después de registrada la revocación también se rechaza: es
// conservador y el alumno puede reintentar, ya con la vigencia actualizada.
const VentanaRevocacion = 2 * time.Second

// Revocacion es una baja informada por members-service: una membresía cancelada
// o un alumno dado de baja. EventoID deduplica las reentregas.
type Revocacion struct {
	EventoID    string
	Tipo        string
	AlumnoID    string
	MembresiaID string
	OcurridaEn  time.Time
}

// Motivo devuelve el motivo con que se cancelan las reservas afectadas.
func (r Revocacion) Motivo() string {
	if r.Tipo == RevocacionUsuarioDesactivado {
		return MotivoAlumnoDadoDeBaja
	}
	return MotivoMembresiaCancelada
}

// Error devuelve el error con que se rechaza una reserva que compitió con esta
// revocación (CL-19).
func (r Revocacion) Error() error {
	if r.Tipo == RevocacionUsuarioDesactivado {
		return ErrUsuarioInactivo
	}
	return ErrMembresiaNoVigente
}

// FechasDeReservas devuelve las fechas distintas, ordenadas, de las clases de
// las reservas, para consultar su cobertura a members-service.
func FechasDeReservas(reservas []Reserva) []string {
	vistas := map[string]bool{}
	var fechas []string
	for _, r := range reservas {
		if f := r.Clase.Fecha(); !vistas[f] {
			vistas[f] = true
			fechas = append(fechas, f)
		}
	}
	sort.Strings(fechas)
	return fechas
}

// ReservasACancelar aplica RN-04 y RN-34 sobre las reservas futuras confirmadas
// del alumno. Una baja del alumno cancela todas. Una membresía cancelada cancela
// las que ya no tienen otra membresía vigente que cubra su fecha (CL-12): las que
// siguen cubiertas se conservan.
func (r Revocacion) ReservasACancelar(reservas []Reserva, v Vigencia) []string {
	var ids []string
	for _, reserva := range reservas {
		if reserva.Estado != ReservaConfirmada {
			continue
		}
		cubierta := r.Tipo == RevocacionMembresiaCancelada && v.AlumnoActivo && v.VigenteEn[reserva.Clase.Fecha()]
		if !cubierta {
			ids = append(ids, reserva.ID)
		}
	}
	return ids
}
