// Búsqueda por texto parcial (corrección del 13/11/2026, HU-13).
package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPalabrasBusqueda(t *testing.T) {
	casos := []struct {
		texto string
		want  []string
	}{
		{"", nil},
		{"   ", nil},
		{"zum", []string{"zum"}},
		{"ZUM", []string{"zum"}},
		{"Musculación", []string{"musculacion"}},
		{"  funcional,  HIIT ", []string{"funcional", "hiit"}},
		{"Peña-Ñandú", []string{"pena", "nandu"}},
		{"spinning 2", []string{"spinning", "2"}},
		{"%_*", nil},
	}
	for _, c := range casos {
		got, err := PalabrasBusqueda(c.texto)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("PalabrasBusqueda(%q) = %v, %v; want %v", c.texto, got, err, c.want)
		}
	}
	if _, err := PalabrasBusqueda(strings.Repeat("a", MaxRunasBusqueda)); err != nil {
		t.Errorf("60 caracteres se permiten: %v", err)
	}
	if _, err := PalabrasBusqueda(strings.Repeat("a", MaxRunasBusqueda+1)); !errors.Is(err, ErrValidacion) {
		t.Errorf("61 caracteres es VALIDACION: %v", err)
	}
	if _, err := PalabrasBusqueda("a b c d e f"); !errors.Is(err, ErrValidacion) {
		t.Errorf("6 palabras es VALIDACION: %v", err)
	}
}
