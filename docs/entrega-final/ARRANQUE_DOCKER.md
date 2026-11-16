# Arranque confiable con Docker

Fecha: 14/11/2026. Relación: enunciado §2.12 (procedimiento único, automatizado y sin configuración manual).

## 1. Contexto

El repositorio se migró a una carpeta nueva y se borró Docker por completo para arrancar de cero. En ese arranque limpio, `make demo` falló varias veces por cuatro problemas que no aparecían con volúmenes ya creados. Ninguno afecta el código de los servicios: están en `deploy/docker-compose.yml` y en el `Makefile`.

## 2. Problemas y correcciones

| # | Síntoma | Causa | Corrección |
|---|---|---|---|
| 1 | El mock de partners nunca quedaba sano y `make demo` siempre terminaba con error | Su healthcheck consultaba `/v1/benefits`, ruta que dejó de existir con el contrato v2 | El chequeo consulta `/v2/accounts/healthcheck/balance`, ruta del contrato vigente |
| 2 | RabbitMQ se reiniciaba en el primer arranque (`.erlang.cookie: eacces`) | El healthcheck `rabbitmq-diagnostics` corría como root. La imagen usa `HOME=/var/lib/rabbitmq`: si el chequeo llegaba antes que el servidor, creaba la cookie con dueño root y permisos 0400, y el servidor (usuario `rabbitmq`) no podía leerla | Primero, el chequeo pasó a correr como el usuario `rabbitmq` con `gosu`. El 15/11/2026 se reemplazó por un chequeo TCP del puerto AMQP (ver la sección 2.1) |
| 3 | Members o Benefits terminaban con «connection refused» contra PostgreSQL aunque figuraba sano | En el primer arranque, la imagen corre `deploy/postgres/init` con un servidor temporal que solo escucha el socket local, y `pg_isready` sin `-h` lo daba por listo | `pg_isready -h 127.0.0.1`: por TCP, como se conectan los servicios |
| 4 | Misma carrera en MongoDB | El init (`create-users.js`) usa un `mongod` temporal que solo escucha 127.0.0.1, y el chequeo preguntaba ahí | El chequeo pregunta a la IP del contenedor (`hostname -i`) |

Dos correcciones relacionadas:

- **`hostname: rabbitmq`.** RabbitMQ guarda los datos en `mnesia/rabbit@<hostname>`. Con el hostname aleatorio que asigna Docker, recrear el contenedor dejaba las colas durables en una carpeta que el nodo nuevo no leía.
- **`make down` y `make clean` con los perfiles `search` e `identity`.** Sin ellos no bajaban OpenSearch ni Mailpit, que `make demo` sí levanta. El índice y los correos viejos sobrevivían a un `make clean`.

### 2.1 Chequeo de RabbitMQ por TCP (15/11/2026)

Durante el test de carga (D13) se vio que `rabbitmq-diagnostics`, aun como el usuario `rabbitmq`, arrancaba una máquina virtual de Erlang en cada chequeo (cada 5 s), con picos de 140 a 200 % de CPU con el broker en reposo. Esos picos ensuciaban las mediciones de CPU de los demás contenedores.

El chequeo ahora solo comprueba que el puerto AMQP acepte conexiones: `bash -c "echo > /dev/tcp/127.0.0.1/5672"`. RabbitMQ abre ese puerto al terminar de arrancar, así que el criterio de «listo» para los servicios que dependen de él es el mismo. Como no ejecuta herramientas de Erlang, tampoco puede crear la `.erlang.cookie`: el problema 2 queda resuelto por otra vía y ya no hace falta `gosu`.

Verificado con el stack levantado (contenedor `healthy` y sin reinicios durante el test de carga). La verificación de arranque desde cero de la sección 3 se hizo con el chequeo anterior y conviene repetirla con este.

### 2.2 Chequeo de MongoDB por TCP (16/11/2026)

El chequeo con `mongosh` arrancaba un proceso de Node.js cada 5 s: MongoDB promediaba **61 % de CPU en reposo** (picos de 133 %) y ensuciaba las mediciones de carga (CARGA_D13 §5.4). Ahora el chequeo abre una conexión TCP al puerto 27017 en la IP del contenedor (`bash -c 'echo > /dev/tcp/$(hostname -i)/27017'`). Conserva la corrección del problema 4: durante el init, el `mongod` temporal solo escucha 127.0.0.1, así que el chequeo no da sano antes de tiempo. Medido después del cambio: **5 % de CPU en reposo** (máximo 22 %).

### 2.3 Fin de línea de las migraciones (16/11/2026)

Con `core.autocrlf=true`, Git escribía los `.sql` con CRLF en Windows y con LF en Linux. Cada servicio guarda una huella de las migraciones aplicadas y no arranca si el archivo cambia, así que cambiar de rama o de máquina podía dejar a Booking, Members o Benefits sin arrancar. Ahora:

- `.gitattributes` fija `*.sql text eol=lf`: todas las máquinas ven el mismo contenido.
- `pkg/migracion` calcula la huella con LF y acepta la huella guardada con LF, con CRLF o del archivo tal cual. Las bases creadas antes de esta regla, en Windows o en Linux, siguen arrancando, y un cambio real del contenido se sigue rechazando. Lo usan los tres servicios con migraciones, con tests propios.

Verificado contra las bases locales existentes, con huellas CRLF y LF: Booking, Members y Benefits arrancaron con los archivos en LF.

## 3. Verificación

El 14/11/2026, partiendo de Docker sin contenedores ni volúmenes, se corrió `make clean` seguido de `make demo` cuatro veces seguidas con las cuatro correcciones aplicadas. En todas, `make demo` terminó sin errores, con los 17 contenedores que tienen healthcheck en `healthy` y **ningún reinicio**. Jaeger y el OTel Collector no tienen healthcheck.

Antes de las correcciones, el arranque desde cero fallaba en la mayoría de los intentos. Con solo la corrección de RabbitMQ, falló una de cuatro corridas por la carrera de PostgreSQL (problema 3).

Al apagar y volver a prender PostgreSQL y OpenSearch, el sistema vuelve solo a `ready` en unos 30 s, el tiempo de arranque de OpenSearch.

El 16/11/2026, con los chequeos TCP de RabbitMQ y MongoDB, se repitió el arranque desde cero dos veces en un proyecto aislado (`docker compose -p gymverif`, volúmenes nuevos, sin tocar los datos del proyecto `gym`): `up --wait` terminó sin errores en 153 y 176 s, con los 18 contenedores que tienen chequeo en `healthy` y ningún reinicio.

## 4. Cómo arrancar de cero

```bash
make clean                             # baja todo y borra los datos del proyecto
make demo                              # levanta todo y espera los health checks
go run scripts/cargar-datos-demo.go    # opcional: datos de prueba
```

`make demo` no pisa un `.env` existente. Si lo regenera `-forzar`, las contraseñas nuevas no coinciden con volúmenes viejos: correr antes `make clean`.
