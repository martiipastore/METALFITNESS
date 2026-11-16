package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/correlation"
)

// BookingHTTP consulta a booking-service por su API interna; members no lee
// booking_db (regla 3). Prueba las instancias en orden y usa la primera que
// responde, porque las dos comparten la misma base (D-99).
type BookingHTTP struct {
	URLs   []string
	Secret string
	Client *http.Client
}

// FutureClasses devuelve cuántas clases futuras PROGRAMADA tiene el profesor
// (RN-34, CA-02.3).
func (b BookingHTTP) FutureClasses(ctx context.Context, teacherID string) (int, error) {
	var last error = errors.New("booking sin instancias configuradas")
	for _, base := range b.URLs {
		base = strings.TrimRight(strings.TrimSpace(base), "/")
		if base == "" {
			continue
		}
		n, err := b.agenda(ctx, base, teacherID)
		if err == nil {
			return n, nil
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return 0, last
}

func (b BookingHTTP) agenda(ctx context.Context, base, teacherID string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/internal/v1/profesores/"+url.PathEscape(teacherID)+"/agenda", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Internal-Service-Key", b.Secret)
	req.Header.Set(correlation.Header, correlation.FromContext(ctx))
	res, err := b.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("booking no disponible")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("booking respondió %d", res.StatusCode)
	}
	var out struct {
		Programadas *int `json:"clases_programadas"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&out); err != nil || out.Programadas == nil || *out.Programadas < 0 {
		return 0, fmt.Errorf("respuesta de booking inválida")
	}
	return *out.Programadas, nil
}
