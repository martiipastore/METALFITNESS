package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Búsqueda de clases (etapa 8 de la entrega final, ADR-014, D-45). El índice es
// un modelo de lectura: nunca decide cupo, permisos ni reservas (RN-14).

// MaxProfundidadBusqueda limita pagina×tamano (decisión del equipo del
// 10/11/2026: 10.000 resultados, el límite por defecto del motor).
const MaxProfundidadBusqueda = 10000

// ValidarProfundidad rechaza páginas que superan la profundidad aprobada.
func (f FiltroClases) ValidarProfundidad() error {
	if f.Pagina < 1 || f.Tamano < 1 || int64(f.Pagina)*int64(f.Tamano) > MaxProfundidadBusqueda {
		return ErrValidacion
	}
	return nil
}

// HoraInicio es el "HH:MM" local del inicio, el valor que filtra franja (HU-13).
func (c Clase) HoraInicio() string { return c.Inicio.In(Zona).Format("15:04") }

// EventoClase es clase.actualizada traducido por el adaptador AMQP: el estado
// completo de la clase en una versión, sin envelope ni JSON.
type EventoClase struct {
	Clase      Clase
	OcurridoEn time.Time
}

// Valido verifica los campos que el índice necesita para filtrar y ordenar.
func (e EventoClase) Valido() bool {
	c := e.Clase
	estados := c.Estado == ClaseProgramada || c.Estado == ClaseCancelada || c.Estado == ClaseFinalizada
	return c.ID != "" && c.ActividadID != "" && c.ActividadNombre != "" && c.ProfesorID != "" && c.Version >= 1 &&
		estados && !c.Inicio.IsZero() && c.Fin.After(c.Inicio) && c.Capacidad >= 1 && c.Ocupacion >= 0 &&
		c.Ocupacion <= c.Capacidad && !e.OcurridoEn.IsZero()
}

// Búsqueda por texto parcial (corrección de búsqueda del 13/11/2026, HU-13):
// "zum" encuentra Zumba. Cada palabra de la consulta debe ser el comienzo de
// alguna palabra del nombre de la actividad o del profesor, sin distinguir
// mayúsculas ni tildes. OpenSearch y PostgreSQL aplican la misma regla.
const (
	MaxRunasBusqueda    = 60
	MaxPalabrasBusqueda = 5
)

// plegado lleva las letras acentuadas del español a su forma sin tilde, igual
// que el filtro asciifolding del analizador del índice.
var plegado = map[rune]rune{'á': 'a', 'é': 'e', 'í': 'i', 'ó': 'o', 'ú': 'u', 'ü': 'u', 'ñ': 'n', 'à': 'a', 'è': 'e', 'ì': 'i', 'ò': 'o', 'ù': 'u'}

// PalabrasBusqueda normaliza el texto: minúsculas, sin tildes y separado en
// palabras de letras o dígitos. Un texto vacío no filtra. Más de 60 caracteres
// o de 5 palabras es ErrValidacion.
func PalabrasBusqueda(texto string) ([]string, error) {
	if utf8.RuneCountInString(texto) > MaxRunasBusqueda {
		return nil, ErrValidacion
	}
	var palabras []string
	var actual strings.Builder
	cerrar := func() {
		if actual.Len() > 0 {
			palabras = append(palabras, actual.String())
			actual.Reset()
		}
	}
	for _, r := range strings.ToLower(texto) {
		if p, ok := plegado[r]; ok {
			r = p
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			actual.WriteRune(r)
			continue
		}
		cerrar()
	}
	cerrar()
	if len(palabras) > MaxPalabrasBusqueda {
		return nil, ErrValidacion
	}
	return palabras, nil
}
