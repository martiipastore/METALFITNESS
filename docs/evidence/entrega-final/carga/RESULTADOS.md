# Resultados de carga del flujo principal (D13)

Generado por `tests/load/run-flujo.go` el 2026-11-15T22:25:51Z. Ambiente en `environment.json`; datos completos en `resultados.json`.

Escalera de usuarios simultáneos sin pausa, a través del gateway con las dos instancias de Booking. Percentiles de la ventana de medición (sin calentamiento). Corte: p95 > 500 ms o errores > 1 %.

| Escenario | Usuarios | Ops/s | p50 ms | p95 ms | p99 ms | Errores | Reparto booking-1 / booking-2 | Origen de búsquedas (fuente/caché/vencida) | CPU máx. (contenedor) |
|---|---|---|---|---|---|---|---|---|---|
| busqueda | 10 | 794.0 | 9 | 28 | 49 | 0.00 % | 38192 / 38191 | 86 / 76297 / 0 | 142 % (api-gateway) |
| busqueda | 25 | 572.3 | 33 | 105 | 184 | 0.00 % | 21256 / 21256 | 174 / 42338 / 0 | 173 % (api-gateway) |
| busqueda | 50 | 659.4 | 60 | 172 | 254 | 0.00 % | 33247 / 33248 | 199 / 66296 / 0 | 183 % (api-gateway) |
| busqueda | 100 | 1214.9 | 68 | 167 | 331 | 0.00 % | 51115 / 51115 | 568 / 101662 / 0 | 202 % (api-gateway) |
| mezcla | 10 | 540.0 | 11 | 47 | 91 | 0.00 % | 20253 / 20253 | 7073 / 29440 / 0 | 215 % (opensearch) |
| mezcla | 25 | 941.8 | 20 | 62 | 107 | 0.00 % | 36337 / 36336 | 4623 / 60814 / 0 | 154 % (api-gateway) |
| mezcla | 50 | 1269.2 | 33 | 85 | 133 | 0.00 % | 53497 / 53497 | 173 / 96168 / 0 | 176 % (api-gateway) |
| mezcla | 100 | 534.7 | 146 | 438 | 840 | 0.07 % | 32664 / 32664 | 812 / 57888 / 0 | 296 % (opensearch) |
| pico | 100 | — | 1671 | 1766 | 1783 | 0.00 % | 50 / 50 | — (solo reservas) | — (ráfaga más corta que el muestreo) |
| pico | 100 | — | 1245 | 1508 | 1587 | 0.00 % | 50 / 50 | — (solo reservas) | — (ráfaga más corta que el muestreo) |
| pico | 100 | — | 906 | 1015 | 1055 | 0.00 % | 50 / 50 | — (solo reservas) | — (ráfaga más corta que el muestreo) |
| caida | 25 | 673.8 | 26 | 69 | 115 | 0.94 % | 53767 / 27348 | n/d (la instancia reiniciada pone sus contadores en cero) | 194 % (api-gateway) |

## Picos de reservas e integridad

| Pico | Confirmadas | 409 de negocio | Errores | p95 reserva ms | Capacidad | Ocupación | Reservas confirmadas | Clases sobre cupo | Duplicadas |
|---|---|---|---|---|---|---|---|---|---|
| 1 | 30 | 70 | 0 | 1766 | 30 | 30 | 30 | 0 | 0 |
| 2 | 30 | 70 | 0 | 1508 | 30 | 30 | 30 | 0 | 0 |
| 3 | 30 | 70 | 0 | 1015 | 30 | 30 | 30 | 0 | 0 |

## Caída de una instancia durante la carga

Mezcla con 25 usuarios; sucesos: 22:40:16.727 (+22.3 s) docker kill gym-booking-service-2-1; 22:40:53.038 (+58.6 s) docker start booking-service-2. Errores 570 de 60642 (0.94 %), por tipo: map[medicion_errores{status:502}:530 medicion_errores{status:504}:40]. Reparto: map[booking-service-1:53767 booking-service-2:27348].

## Recursos por escenario (CPU promedio / máxima, memoria máxima)

- **busqueda, 10 usuarios:** api-gateway 112/142 % 14 MiB; booking-service-2 71/88 % 20 MiB; booking-service 69/91 % 19 MiB; mongo 48/122 % 221 MiB; postgres 39/57 % 135 MiB
- **busqueda, 25 usuarios:** api-gateway 120/173 % 17 MiB; mongo 87/118 % 204 MiB; booking-service-2 78/122 % 22 MiB; booking-service 76/108 % 20 MiB; postgres 44/61 % 135 MiB
- **busqueda, 50 usuarios:** api-gateway 127/183 % 21 MiB; mongo 82/127 % 223 MiB; booking-service 77/111 % 21 MiB; booking-service-2 77/105 % 22 MiB; postgres 48/70 % 136 MiB
- **busqueda, 100 usuarios:** api-gateway 171/202 % 31 MiB; booking-service-2 104/117 % 25 MiB; booking-service 101/117 % 24 MiB; postgres 66/74 % 135 MiB; redis 42/47 % 26 MiB
- **mezcla, 10 usuarios:** api-gateway 104/142 % 21 MiB; booking-service-2 77/99 % 23 MiB; booking-service 73/94 % 22 MiB; postgres 61/86 % 142 MiB; opensearch 55/215 % 947 MiB
- **mezcla, 25 usuarios:** api-gateway 145/154 % 20 MiB; booking-service-2 85/96 % 22 MiB; booking-service 77/87 % 22 MiB; mongo 61/110 % 225 MiB; postgres 55/66 % 143 MiB
- **mezcla, 50 usuarios:** api-gateway 136/176 % 23 MiB; booking-service-2 76/99 % 23 MiB; booking-service 71/91 % 22 MiB; mongo 66/102 % 216 MiB; postgres 48/60 % 142 MiB
- **mezcla, 100 usuarios:** api-gateway 115/174 % 33 MiB; opensearch 81/296 % 951 MiB; booking-service-2 73/99 % 25 MiB; postgres 63/98 % 145 MiB; booking-service 62/101 % 24 MiB
- **caida, 25 usuarios:** api-gateway 130/194 % 23 MiB; booking-service 93/172 % 23 MiB; booking-service-2 62/103 % 23 MiB; postgres 53/77 % 144 MiB; redis 42/129 % 48 MiB
