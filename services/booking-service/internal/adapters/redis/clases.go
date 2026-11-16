package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/ports"
)

// Claves de la caché de búsquedas de clases (ADR-007 v2). La generación es un
// valor aleatorio: invalidar la reemplaza y las copias anteriores quedan
// inalcanzables hasta vencer. Si Redis pierde la generación, se crea otra y
// todas las búsquedas vuelven a la fuente. Además se guarda la última copia de
// cada búsqueda fuera de las generaciones: solo se sirve si la fuente no
// responde (copia vencida), nunca en lugar de consultarla.
const (
	ClaveGeneracionClases = "booking:clases:v1:generacion"
	prefijoClases         = "booking:clases:v1:"
	// prefijoUltima guarda la última copia de cada búsqueda, fuera de las
	// generaciones; las generaciones son hexadecimales y nunca valen "ultima".
	prefijoUltima = "booking:clases:v1:ultima:"
)

var _ ports.CacheClases = (*Clases)(nil)

// MetricasClases separa aciertos, faltantes y errores (teórico, clase 3 §2.1):
// un error de Redis no es un miss.
type MetricasClases struct {
	Aciertos, Faltantes, Errores, Invalidaciones atomic.Int64
}

// Clases implementa ports.CacheClases sobre Redis. Retencion es cuánto vive la
// copia en Redis: la vigencia fresca más el tiempo adicional en que puede
// servirse vencida si la fuente no responde.
type Clases struct {
	Client    *goredis.Client
	Retencion time.Duration
	Timeout   time.Duration
	Metricas  *MetricasClases
}

type claseCopia struct {
	ID              string    `json:"id"`
	ActividadID     string    `json:"actividad_id"`
	ActividadNombre string    `json:"actividad_nombre"`
	Musculacion     bool      `json:"musculacion"`
	HorarioID       string    `json:"horario_id"`
	Inicio          time.Time `json:"inicio"`
	Fin             time.Time `json:"fin"`
	ProfesorID      string    `json:"profesor_id"`
	ProfesorNombre  string    `json:"profesor_nombre"`
	Capacidad       int       `json:"capacidad"`
	Ocupacion       int       `json:"ocupacion"`
	Estado          string    `json:"estado"`
	Version         int64     `json:"version"`
}

type paginaCopia struct {
	Items        []claseCopia `json:"items"`
	Total        int          `json:"total"`
	Pagina       int          `json:"pagina"`
	Tamano       int          `json:"tamano"`
	ConsultadoEn time.Time    `json:"consultado_en"`
}

func (c *Clases) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.Timeout)
}

func (c *Clases) error(err error) error {
	if err != nil {
		c.Metricas.Errores.Add(1)
	}
	return err
}

// generacion lee la generación vigente y la crea si no existe.
func (c *Clases) generacion(ctx context.Context) (string, error) {
	g, err := c.Client.Get(ctx, ClaveGeneracionClases).Result()
	if err == nil {
		return g, nil
	}
	if !errors.Is(err, goredis.Nil) {
		return "", err
	}
	if err = c.Client.SetNX(ctx, ClaveGeneracionClases, nueva(), 0).Err(); err != nil {
		return "", err
	}
	return c.Client.Get(ctx, ClaveGeneracionClases).Result()
}

func nueva() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Clases) LeerClases(ctx context.Context, clave string) (domain.PaginaClases, string, bool, error) {
	ctx, cancel := c.ctx(ctx)
	defer cancel()
	g, err := c.generacion(ctx)
	if err != nil {
		return domain.PaginaClases{}, "", false, c.error(err)
	}
	raw, err := c.Client.Get(ctx, prefijoClases+g+":"+clave).Bytes()
	if errors.Is(err, goredis.Nil) {
		c.Metricas.Faltantes.Add(1)
		return domain.PaginaClases{}, g, false, nil
	}
	if err != nil {
		return domain.PaginaClases{}, "", false, c.error(err)
	}
	out, ok := desdeCopia(raw)
	if !ok {
		// Una copia ilegible se trata como faltante y se reemplaza.
		c.Metricas.Faltantes.Add(1)
		return domain.PaginaClases{}, g, false, nil
	}
	c.Metricas.Aciertos.Add(1)
	return out, g, true, nil
}

// LeerUltimaClases lee la última copia de la clave sin importar la generación.
func (c *Clases) LeerUltimaClases(ctx context.Context, clave string) (domain.PaginaClases, bool, error) {
	ctx, cancel := c.ctx(ctx)
	defer cancel()
	raw, err := c.Client.Get(ctx, prefijoUltima+clave).Bytes()
	if errors.Is(err, goredis.Nil) {
		return domain.PaginaClases{}, false, nil
	}
	if err != nil {
		return domain.PaginaClases{}, false, c.error(err)
	}
	out, ok := desdeCopia(raw)
	return out, ok, nil
}

func desdeCopia(raw []byte) (domain.PaginaClases, bool) {
	var copia paginaCopia
	if err := json.Unmarshal(raw, &copia); err != nil {
		return domain.PaginaClases{}, false
	}
	out := domain.PaginaClases{Items: make([]domain.Clase, 0, len(copia.Items)), Total: copia.Total, Pagina: copia.Pagina, Tamano: copia.Tamano, ConsultadoEn: copia.ConsultadoEn}
	for _, i := range copia.Items {
		out.Items = append(out.Items, domain.Clase{ID: i.ID, ActividadID: i.ActividadID, ActividadNombre: i.ActividadNombre, Musculacion: i.Musculacion, HorarioID: i.HorarioID,
			Inicio: i.Inicio, Fin: i.Fin, ProfesorID: i.ProfesorID, ProfesorNombre: i.ProfesorNombre, Capacidad: i.Capacidad, Ocupacion: i.Ocupacion, Estado: i.Estado, Version: i.Version})
	}
	return out, true
}

func (c *Clases) GuardarClases(ctx context.Context, generacion, clave string, p domain.PaginaClases) error {
	copia := paginaCopia{Items: make([]claseCopia, 0, len(p.Items)), Total: p.Total, Pagina: p.Pagina, Tamano: p.Tamano, ConsultadoEn: p.ConsultadoEn.UTC()}
	for _, i := range p.Items {
		copia.Items = append(copia.Items, claseCopia{ID: i.ID, ActividadID: i.ActividadID, ActividadNombre: i.ActividadNombre, Musculacion: i.Musculacion, HorarioID: i.HorarioID,
			Inicio: i.Inicio.UTC(), Fin: i.Fin.UTC(), ProfesorID: i.ProfesorID, ProfesorNombre: i.ProfesorNombre, Capacidad: i.Capacidad, Ocupacion: i.Ocupacion, Estado: i.Estado, Version: i.Version})
	}
	raw, err := json.Marshal(copia)
	if err != nil {
		return err
	}
	ctx, cancel := c.ctx(ctx)
	defer cancel()
	// La copia de la generación vigente y la última conocida, en un solo viaje.
	pipe := c.Client.TxPipeline()
	pipe.Set(ctx, prefijoClases+generacion+":"+clave, raw, c.Retencion)
	pipe.Set(ctx, prefijoUltima+clave, raw, c.Retencion)
	_, err = pipe.Exec(ctx)
	return c.error(err)
}

func (c *Clases) InvalidarClases(ctx context.Context) error {
	ctx, cancel := c.ctx(ctx)
	defer cancel()
	if err := c.Client.Set(ctx, ClaveGeneracionClases, nueva(), 0).Err(); err != nil {
		return c.error(err)
	}
	c.Metricas.Invalidaciones.Add(1)
	return nil
}
