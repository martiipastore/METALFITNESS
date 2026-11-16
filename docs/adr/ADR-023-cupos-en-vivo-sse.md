# ADR-023 — Cupos en vivo con Server-Sent Events (bonus D-40)

| Campo | Valor |
|---|---|
| **Estado** | Implementado y verificado el 16/11/2026; diseño elegido por la integrante a cargo, revisión del equipo pendiente |
| **Fecha** | 2026-11-16 |
| **Decisión** | D-103 |
| **Relacionado con** | D-40 (bonus anunciado en la Entrega 2), SPEC §1.6, HU-13; ADR-014 (`clase.actualizada`), ADR-012 v3 (dos instancias), ADR-006 (identidad en el gateway) |

## Contexto

En la Entrega 2 el equipo anunció como desafío bonus «actualizar la ocupación mostrada mediante Server-Sent Events a través del gateway» (D-40). Hasta ahora la pantalla de Clases recargaba la lista cada 30 s. Un alumno podía ver «3 lugares libres» cuando ya no quedaba ninguno, y solo se enteraba al reservar (`SIN_CUPO`).

Booking ya publica `clase.actualizada` por outbox en cada cambio de una clase: un trigger lo escribe en la misma transacción de la reserva, la cancelación, el cambio de cupos, la cancelación de la clase, una baja (D-98) o el cierre automático.

## Alternativas

| Alternativa | Ventaja | Desventaja |
|---|---|---|
| Recarga periódica más frecuente | Sin cambios de backend | Más carga en búsqueda y caché; sigue atrasada |
| WebSocket | Bidireccional | El navegador no necesita mandar nada; agrega un protocolo y su autenticación |
| **Server-Sent Events (elegida)** | HTTP común, unidireccional, reconexión simple; pasa por el gateway con el mismo JWT y control de tráfico | Una conexión abierta por pestaña; `EventSource` no manda headers, así que el cliente usa `fetch` |

Para el origen de los cambios se consideró Redis pub/sub. Se eligió el evento que ya existe: no agrega un mecanismo nuevo y cubre todos los caminos que cambian una clase, porque viene del trigger.

## Decisión

- **Booking** expone `GET /api/v1/classes/stream` (`text/event-stream`), con la misma identidad que el resto de la API pero fuera del grupo con timeout de 4 s. Envía `event: ocupacion` con `{clase_id, ocupacion, capacidad, estado, version}`, sin datos de personas; un latido cada 15 s; y corta a los 10 min con `event: fin` para que el navegador se reconecte y vuelva a balancearse. Máximo 500 conexiones por instancia (después, 503 con `Retry-After`).
- **Cada instancia** escucha `clase.actualizada` en una cola temporal propia (exclusiva, sin durabilidad, autoeliminable) atada a `gym.events`. Un cambio hecho en una instancia llega a los navegadores conectados a la otra. No hay reintentos ni DLQ: perder un aviso solo atrasa la pantalla.
- **Un suscriptor lento** pierde mensajes (búfer de 32) en lugar de frenar a los demás.
- **Gateway:** la ruta del stream queda sin el timeout de 3 s de `/api/v1/classes`; sigue autenticada y con control de tráfico. El proxy de Go reenvía cada evento al llegar.
- **Web:** la pantalla de Clases se suscribe con `fetch` desde `api/client.ts` (el único cliente HTTP), aplica un cambio solo si su `version` es mayor que la ya vista, recalcula lugares libres y si se puede reservar, y resalta la clase con una animación de CSS, estática con `prefers-reduced-motion`. Muestra el estado «Cupos en vivo» y, si se corta, reintenta con espera creciente (2 s a 30 s) y resincroniza la lista. La recarga cada 30 s sigue como respaldo.
- **Salud:** la suscripción es una dependencia parcial (`cupos-en-vivo`): si cae RabbitMQ, Booking informa `degraded` y todo lo demás sigue igual.

## Consecuencias

- La reserva se sigue validando en PostgreSQL; el stream solo mantiene la pantalla al día (no cambia RN-14).
- El retraso medido es de alrededor de 1 s (el relay del outbox publica cada 0,5 s).
- Solo local: Vercel no tiene Booking ni RabbitMQ (ADR-021).
- SSE es una herramienta adicional al teórico (no se localizó en el apunte). Se apoya en conceptos de clase ya usados: eventos publicados por outbox y su consumo (pp. 27–28 y 103–105) y el punto de entrada único que autentica y controla el tráfico. Ver docs/BASE_TEORICA.md.
