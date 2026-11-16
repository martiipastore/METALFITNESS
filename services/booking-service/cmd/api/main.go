// Comando api de booking-service: actividades, clases, reservas y asistencia
// (patrón Hexagonal). Acá solo se cablean adaptadores, puertos y casos de uso.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // zona America/Argentina/Buenos_Aires aunque la imagen no la traiga

	"github.com/GuadiViti/Arquitectura-de-Software/pkg/correlation"
	"github.com/GuadiViti/Arquitectura-de-Software/pkg/health"
	"github.com/GuadiViti/Arquitectura-de-Software/pkg/httpserver"
	"github.com/GuadiViti/Arquitectura-de-Software/pkg/logger"
	"github.com/GuadiViti/Arquitectura-de-Software/pkg/messaging"
	"github.com/GuadiViti/Arquitectura-de-Software/pkg/telemetry"
	bookingamqp "github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/amqp"
	bookinghttp "github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/http"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/members"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/opensearch"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/postgres"
	bookingredis "github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/adapters/redis"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/application"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/config"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/domain"
	"github.com/GuadiViti/Arquitectura-de-Software/services/booking-service/internal/ports"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
)

const serviceName = "booking-service"

type relojSistema struct{}

func (relojSistema) Ahora() time.Time { return time.Now() }

func main() {
	cfg, err := config.Load()
	log := logger.New(serviceName, cfg.LogLevel)
	if err != nil {
		log.Error("configuración inválida", slog.Any("error", err))
		os.Exit(1)
	}
	if err := run(cfg, log); err != nil {
		log.Error("el servicio terminó con error", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// D11: trazas OTLP si OTEL_EXPORTER_OTLP_ENDPOINT está definido.
	shutdownTraces, err := telemetry.Setup(ctx, serviceName, log)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := shutdownTraces(flushCtx); err != nil {
			log.Warn("no se completó el cierre de trazas")
		}
	}()

	db, err := postgres.Open(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	var relay *messaging.Relay
	// Con la caché de búsquedas u OpenSearch, la búsqueda responde sin PostgreSQL
	// (ADR-007 v2, D-95); reservar falla cerrado igual. PostgreSQL pasa a ser
	// parcial para que el balanceador no retire las dos instancias cuando cae la
	// base compartida (jornada de fallas del 15/11/2026, D-100).
	checks := []health.Check{{Name: "postgres", Fn: db.Ping, Partial: cfg.CacheEnabled || cfg.SearchEnabled}}
	if cfg.MessagingEnabled {
		if !cfg.Enabled || cfg.AMQPURL == "" {
			return errors.New("mensajería requiere BOOKING_ENABLED=true y BOOKING_AMQP_URL")
		}
		// La cola del indexer se declara aunque el indexer esté apagado: los
		// clase.actualizada esperan durables y no se pierden (etapa 8).
		relay = &messaging.Relay{URL: cfg.AMQPURL, Log: log, Publish: db.PublicarOutbox, Routes: []messaging.Route{{Queue: "benefits.asistencia-registrada", Key: "asistencia.registrada"}, {Queue: "benefits.inasistencia-registrada", Key: "inasistencia.registrada"}, {Queue: "booking-indexer.clase-actualizada", Key: "clase.actualizada"}}}
		checks = append(checks, health.Check{Name: "rabbitmq", Fn: relay.Ready, Partial: true})
	}
	// Bajas desde Members (RN-04, RN-34, D-98). Las dos instancias consumen la
	// misma cola; la deduplicación por event_id está en booking_db.
	revocador := &revocadorDiferido{}
	var bajas []*messaging.Consumer
	if cfg.MessagingEnabled {
		for _, route := range []messaging.Route{{Queue: "booking.membresia-cancelada", Key: "membresia.cancelada"}, {Queue: "booking.usuario-desactivado", Key: "usuario.desactivado"}} {
			adapter := bookingamqp.Revocaciones{App: revocador, EventType: route.Key}
			consumer := &messaging.Consumer{URL: cfg.AMQPURL, Route: route, Handle: adapter.Handle, Permanent: bookingamqp.PermanentRevocacion, Log: log}
			bajas = append(bajas, consumer)
			checks = append(checks, health.Check{Name: route.Queue, Fn: consumer.Ready, Partial: true})
		}
	}
	var indice *opensearch.Client
	if cfg.SearchEnabled {
		if !cfg.Enabled || !cfg.MessagingEnabled {
			return errors.New("la búsqueda indexada requiere BOOKING_ENABLED=true y BOOKING_MESSAGING_ENABLED=true")
		}
		indice = &opensearch.Client{URL: cfg.OpenSearchURL, HTTP: &http.Client{Timeout: cfg.SearchTimeout}}
		// ADR-014/D-45: sin OpenSearch solo falla la búsqueda; reservar sigue.
		checks = append(checks, health.Check{Name: "opensearch", Fn: indice.Ping, Partial: true})
	}
	var cache *bookingredis.Actividades
	var cacheClases *bookingredis.Clases
	if cfg.CacheEnabled {
		if !cfg.Enabled {
			return errors.New("la caché requiere BOOKING_ENABLED=true")
		}
		client := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DialTimeout: cfg.CacheTimeout, ReadTimeout: cfg.CacheTimeout, WriteTimeout: cfg.CacheTimeout, MaxRetries: -1})
		defer client.Close()
		cache = &bookingredis.Actividades{Client: client, TTL: cfg.CacheTTL, Timeout: cfg.CacheTimeout, Metricas: &bookingredis.Metricas{}}
		cacheClases = &bookingredis.Clases{Client: client, Retencion: cfg.CacheClasesRetencion, Timeout: cfg.CacheTimeout, Metricas: &bookingredis.MetricasClases{}}
		// ADR-007: Redis es parcial; sin él el catálogo se lee de PostgreSQL.
		checks = append(checks, health.Check{Name: "redis", Fn: cache.Ping, Partial: true})
	}
	router := bookinghttp.NewRouter(log, cfg.HealthTimeout, checks...)
	if cache != nil {
		router.GET("/metrics", func(c *gin.Context) {
			m := cache.Metricas
			c.Data(200, "text/plain; version=0.0.4", []byte(fmt.Sprintf("activities_cache_hits_total %d\nactivities_cache_misses_total %d\nactivities_cache_errors_total %d\nactivities_cache_invalidations_total %d\nactivities_cache_redis_operations_total %d\nactivities_cache_redis_seconds_total %.6f\n",
				m.Aciertos.Load(), m.Fallos.Load(), m.Errores.Load(), m.Invalidaciones.Load(), m.Operaciones.Load(), float64(m.MicrosegundosRedis.Load())/1e6)+
				metricasClases(cacheClases.Metricas)))
		})
	}
	if cfg.Enabled {
		if len(cfg.InternalServiceKey) < 32 {
			return errors.New("BOOKING_ENABLED requiere GYM_INTERNAL_SERVICE_KEY de al menos 32 caracteres")
		}
		if err = db.Migrate(ctx); err != nil {
			return err
		}
		if relay != nil {
			relayCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			defer func() { cancel(); <-done }()
			go func() { defer close(done); relay.Run(relayCtx) }()
		}
		reloj := relojSistema{}
		membersClient := members.Client{
			URL:    cfg.MembersURL,
			Secret: cfg.InternalServiceKey,
			HTTP:   &http.Client{Timeout: cfg.MembersTimeout, Transport: correlation.Transport{Base: telemetry.Transport{}}},
		}
		servicio := application.New(db, db, membersClient, reloj)
		if cache != nil {
			servicio.ConCache(cache)
			servicio.ConCacheClases(cacheClases, db, cfg.CacheClasesFresco)
			log.Info("caché del catálogo de actividades y de búsquedas de clases habilitada")
		}
		if indice != nil {
			servicio.ConBusqueda(indice, db)
			log.Info("búsqueda de clases en OpenSearch habilitada")
		}
		bookinghttp.Register(router, servicio, cfg.InternalServiceKey, reloj.Ahora)
		if len(bajas) > 0 {
			revocador.app = servicio
			bajasCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{}, len(bajas))
			defer func() {
				cancel()
				for range bajas {
					<-done
				}
			}()
			for _, c := range bajas {
				go func(c *messaging.Consumer) { c.Run(bajasCtx); done <- struct{}{} }(c)
			}
			log.Info("consumo de bajas desde members habilitado")
		}
		go cerrarClases(ctx, log, servicio, cfg.CloseInterval)
		log.Info("reservas habilitadas")
	}
	return httpserver.Run(ctx, log, httpserver.Options{
		Addr:            cfg.HTTPAddr,
		Handler:         router,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
}

// metricasClases expone la caché de búsquedas de clases y el origen de cada
// respuesta de GET /classes (ADR-007 v2), en formato de texto de Prometheus.
func metricasClases(m *bookingredis.MetricasClases) string {
	o := &bookinghttp.OrigenesBusqueda
	return fmt.Sprintf("classes_cache_hits_total %d\nclasses_cache_misses_total %d\nclasses_cache_errors_total %d\nclasses_cache_invalidations_total %d\n"+
		"classes_search_responses_total{origen=\"fuente\"} %d\nclasses_search_responses_total{origen=\"cache\"} %d\nclasses_search_responses_total{origen=\"cache_vencida\"} %d\nclasses_search_unmarked_total %d\n",
		m.Aciertos.Load(), m.Faltantes.Load(), m.Errores.Load(), m.Invalidaciones.Load(),
		o.Fuente.Load(), o.Cache.Load(), o.CacheVencida.Load(), o.SinMarcas.Load())
}

// cerrarClases ejecuta periódicamente el cierre automático de clases terminadas,
// que pasa a AUSENTE las reservas sin ingreso (RN-38, CA-22.8).
func cerrarClases(ctx context.Context, log *slog.Logger, s *application.Servicio, intervalo time.Duration) {
	ticker := time.NewTicker(intervalo)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			clases, ausentes, err := s.CerrarClases(ctx)
			switch {
			case err != nil:
				log.Warn("no se pudieron cerrar las clases terminadas", slog.Any("error", err))
			case clases > 0:
				log.Info("clases finalizadas", slog.Int("clases", clases), slog.Int("ausentes", ausentes))
			}
		}
	}
}

// revocadorDiferido permite declarar los consumidores (y su chequeo de salud)
// antes de construir el servicio de aplicación, que depende de la base migrada.
type revocadorDiferido struct{ app *application.Servicio }

func (r *revocadorDiferido) Revocar(ctx context.Context, rev domain.Revocacion) (int, error) {
	if r.app == nil {
		return 0, ports.ErrNoDisponible
	}
	return r.app.Revocar(ctx, rev)
}
