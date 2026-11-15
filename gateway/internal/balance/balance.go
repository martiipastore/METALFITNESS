// Package balance reparte las solicitudes de un servicio entre sus instancias
// (etapa 12, parte B; enunciado §2.3; ADR-012 v3). Round-robin entre las
// instancias elegibles, con igual peso y sin afinidad (D-46). Un chequeo activo
// de /health/ready decide la elegibilidad: una instancia sale tras 3 fallos
// consecutivos y vuelve tras 2 éxitos consecutivos (D-51); un resultado
// contrario reinicia la cuenta. Así una demora aislada no la retira ni una
// recuperación aislada la reincorpora (flapping; teórico, clase 6 §2.3).
package balance

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Politica de los chequeos (D-46 y D-51).
type Politica struct {
	Intervalo, Timeout     time.Duration
	FallosParaRetirar      int
	ExitosParaReincorporar int
}

// PoliticaAprobada son los valores de D-46 y D-51.
var PoliticaAprobada = Politica{Intervalo: 5 * time.Second, Timeout: 2 * time.Second, FallosParaRetirar: 3, ExitosParaReincorporar: 2}

// ErrSinInstancias indica que ninguna instancia del servicio es elegible.
var ErrSinInstancias = errors.New("ninguna instancia disponible")

// Instancia es un destino del servicio con su estado de elegibilidad.
type Instancia struct {
	Nombre string
	URL    *url.URL

	mu                  sync.Mutex
	elegible            bool
	fallos, exitos      int
	Solicitudes         atomic.Int64
	Retiros, Reingresos atomic.Int64
}

// Elegible indica si la instancia recibe tráfico.
func (i *Instancia) Elegible() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.elegible
}

// Balanceador es el conjunto de instancias de un servicio.
type Balanceador struct {
	Servicio   string
	Instancias []*Instancia
	politica   Politica
	client     *http.Client
	log        *slog.Logger
	siguiente  atomic.Uint64
}

// New crea el balanceador. Las instancias arrancan elegibles: el primer chequeo
// corre enseguida y, si una no responde, sale tras FallosParaRetirar chequeos.
func New(servicio string, urls []*url.URL, politica Politica, client *http.Client, log *slog.Logger) *Balanceador {
	b := &Balanceador{Servicio: servicio, politica: politica, client: client, log: log}
	for n, u := range urls {
		b.Instancias = append(b.Instancias, &Instancia{Nombre: NombreInstancia(servicio, n, len(urls)), URL: u, elegible: true})
	}
	return b
}

// NombreInstancia identifica la instancia en logs, métricas y /api/v1/status:
// el servicio si hay una sola; servicio-1, servicio-2… si hay varias.
func NombreInstancia(servicio string, n, total int) string {
	if total <= 1 {
		return servicio
	}
	return servicio + "-" + strconv.Itoa(n+1)
}

// Elegir devuelve la próxima instancia elegible en orden circular. Cuenta la
// solicitud para mostrar la distribución en las métricas.
func (b *Balanceador) Elegir() (*Instancia, error) {
	total := len(b.Instancias)
	inicio := b.siguiente.Add(1) - 1
	for k := 0; k < total; k++ {
		i := b.Instancias[(inicio+uint64(k))%uint64(total)]
		if i.Elegible() {
			i.Solicitudes.Add(1)
			return i, nil
		}
	}
	return nil, ErrSinInstancias
}

// Run chequea todas las instancias cada Intervalo hasta que ctx termine.
func (b *Balanceador) Run(ctx context.Context) {
	b.ChequearTodas(ctx)
	ticker := time.NewTicker(b.politica.Intervalo)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.ChequearTodas(ctx)
		}
	}
}

// ChequearTodas corre un chequeo por instancia, en paralelo.
func (b *Balanceador) ChequearTodas(ctx context.Context) {
	var wg sync.WaitGroup
	for _, i := range b.Instancias {
		wg.Add(1)
		go func(i *Instancia) {
			defer wg.Done()
			b.Registrar(i, b.sana(ctx, i))
		}(i)
	}
	wg.Wait()
}

// sana: 200 de /health/ready (ready o degraded, ADR-007) dentro del timeout.
func (b *Balanceador) sana(ctx context.Context, i *Instancia) bool {
	ctx, cancel := context.WithTimeout(ctx, b.politica.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(i.URL.String(), "/")+"/health/ready", nil)
	if err != nil {
		return false
	}
	res, err := b.client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// Registrar aplica un resultado de chequeo a la política 3/2.
func (b *Balanceador) Registrar(i *Instancia, sana bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if sana {
		i.fallos = 0
		if i.elegible {
			return
		}
		i.exitos++
		if i.exitos >= b.politica.ExitosParaReincorporar {
			i.elegible, i.exitos = true, 0
			i.Reingresos.Add(1)
			b.log.Info("instancia reincorporada al balanceo", slog.String("upstream", b.Servicio), slog.String("instancia", i.Nombre))
		}
		return
	}
	i.exitos = 0
	if !i.elegible {
		return
	}
	i.fallos++
	if i.fallos >= b.politica.FallosParaRetirar {
		i.elegible, i.fallos = false, 0
		i.Retiros.Add(1)
		b.log.Warn("instancia retirada del balanceo", slog.String("upstream", b.Servicio), slog.String("instancia", i.Nombre))
	}
}

// Metricas escribe, en formato de texto de Prometheus, la elegibilidad de cada
// instancia y las solicitudes que recibió: la distribución y el estado que pide
// el enunciado (§2.3) se ven en el tablero.
func (b *Balanceador) Metricas() string {
	var s strings.Builder
	for _, i := range b.Instancias {
		up := 0
		if i.Elegible() {
			up = 1
		}
		etiquetas := `{service="` + b.Servicio + `",instancia="` + i.Nombre + `"}`
		s.WriteString("gateway_balancer_instance_up" + etiquetas + " " + strconv.Itoa(up) + "\n")
		s.WriteString("gateway_balancer_requests_total" + etiquetas + " " + strconv.FormatInt(i.Solicitudes.Load(), 10) + "\n")
		s.WriteString("gateway_balancer_removals_total" + etiquetas + " " + strconv.FormatInt(i.Retiros.Load(), 10) + "\n")
		s.WriteString("gateway_balancer_restorations_total" + etiquetas + " " + strconv.FormatInt(i.Reingresos.Load(), 10) + "\n")
	}
	return s.String()
}
