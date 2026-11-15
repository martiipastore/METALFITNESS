# Arranque confiable con Docker

Fecha: 14/11/2026. Relación: enunciado §2.12 (procedimiento único, automatizado y sin configuración manual).

## 1. Contexto

El repositorio se migró a una carpeta nueva y se borró Docker por completo para arrancar de cero. En ese arranque limpio, `make demo` falló varias veces por cuatro problemas que no aparecían con volúmenes ya creados. Ninguno afecta el código de los servicios: están en `deploy/docker-compose.yml` y en el `Makefile`.

## 2. Problemas y correcciones

| # | Síntoma | Causa | Corrección |
|---|---|---|---|
| 1 | El mock de partners nunca quedaba sano y `make demo` siempre terminaba con error | Su healthcheck consultaba `/v1/benefits`, ruta que dejó de existir con el contrato v2 | El chequeo consulta `/v2/accounts/healthcheck/balance`, ruta del contrato vigente |
| 2 | RabbitMQ se reiniciaba en el primer arranque (`.erlang.cookie: eacces`) | El healthcheck `rabbitmq-diagnostics` corría como root. La imagen usa `HOME=/var/lib/rabbitmq`: si el chequeo llegaba antes que el servidor, creaba la cookie con dueño root y permisos 0400, y el servidor (usuario `rabbitmq`) no podía leerla | El chequeo corre como el usuario `rabbitmq` con `gosu` |
| 3 | Members o Benefits terminaban con «connection refused» contra PostgreSQL aunque figuraba sano | En el primer arranque, la imagen corre `deploy/postgres/init` con un servidor temporal que solo escucha el socket local, y `pg_isready` sin `-h` lo daba por listo | `pg_isready -h 127.0.0.1`: por TCP, como se conectan los servicios |
| 4 | Misma carrera en MongoDB | El init (`create-users.js`) usa un `mongod` temporal que solo escucha 127.0.0.1, y el chequeo preguntaba ahí | El chequeo pregunta a la IP del contenedor (`hostname -i`) |

Dos correcciones relacionadas:

- **`hostname: rabbitmq`.** RabbitMQ guarda los datos en `mnesia/rabbit@<hostname>`. Con el hostname aleatorio que asigna Docker, recrear el contenedor dejaba las colas durables en una carpeta que el nodo nuevo no leía.
- **`make down` y `make clean` con los perfiles `search` e `identity`.** Sin ellos no bajaban OpenSearch ni Mailpit, que `make demo` sí levanta. El índice y los correos viejos sobrevivían a un `make clean`.

## 3. Verificación

El 14/11/2026, partiendo de Docker sin contenedores ni volúmenes, se corrió `make clean` seguido de `make demo` cuatro veces seguidas con las cuatro correcciones aplicadas. En todas, `make demo` terminó sin errores, con los 17 contenedores que tienen healthcheck en `healthy` y **ningún reinicio**. Jaeger y el OTel Collector no tienen healthcheck.

Antes de las correcciones, el arranque desde cero fallaba en la mayoría de los intentos. Con solo la corrección de RabbitMQ, falló una de cuatro corridas por la carrera de PostgreSQL (problema 3).

Al apagar y volver a prender PostgreSQL y OpenSearch, el sistema vuelve solo a `ready` en unos 30 s, el tiempo de arranque de OpenSearch.

## 4. Cómo arrancar de cero

```bash
make clean                             # baja todo y borra los datos del proyecto
make demo                              # levanta todo y espera los health checks
go run scripts/cargar-datos-demo.go    # opcional: datos de prueba
```

`make demo` no pisa un `.env` existente. Si lo regenera `-forzar`, las contraseñas nuevas no coinciden con volúmenes viejos: correr antes `make clean`.
