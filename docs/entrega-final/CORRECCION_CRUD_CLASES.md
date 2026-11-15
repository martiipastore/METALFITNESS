# Correcciones de la gestión de clases (administrador)

Fecha: 14/11/2026. Origen: prueba del equipo. Historias: HU-06, HU-07, HU-08, HU-13. Sin cambios de reglas de negocio ni del SPEC.

## 1. Lo que se reportó y lo que pasaba

| Reporte | Causa | El backend |
|---|---|---|
| «No me deja crear una clase nueva y ponerle horario» | Al crear una actividad, el formulario de horarios seguía mostrando la actividad elegida antes (la primera, Musculación). Para ponerle horario a la nueva había que hacer clic en su nombre, y eso no se veía. Además, sin tocar «Generar» en otra sección no se creaba ninguna clase y el alumno no veía nada | Funcionaba: crear actividad, horario y clases respondía 201/200 |
| «Edito los cupos de una clase y al alumno no le cambian» | El botón «Capacidad» de la tabla de actividades cambiaba el **tope** de la actividad, no los cupos de las clases ya generadas. Cada clase tiene su propio cupo (RN-10), que solo se editaba entrando al detalle | Funcionaba: el cupo de una clase llegaba al alumno en alrededor de 1 s |
| (relacionado) El alumno con la pantalla abierta seguía viendo los cupos viejos | La lista de clases no se volvía a consultar sola | — |

El SPEC no contempla editar ni borrar un horario (HU-07 solo lo crea; HU-08 modifica o cancela clases sueltas). El equipo eligió mejorar las pantallas sin agregar ese alcance.

## 2. Cambios

**Actividades y horarios** (`web/src/pages/booking/ActivitiesPage.tsx`):

- Al crear una actividad, queda elegida y la pantalla baja a sus horarios, con el aviso «Ahora agregale un horario».
- Cada fila tiene el botón **Horarios**, y la actividad elegida se marca.
- **Al agregar un horario se generan sus clases** del período de la sección «Generar clases» (por defecto, hoy y 13 días más). Regenerar no duplica (CA-07.5). El botón «Generar» queda para extender el período.
- El botón «Capacidad» pasa a llamarse **Tope**, la columna «Tope por clase», y el diálogo explica que no cambia los cupos de las clases ya generadas.

**Clases** (`web/src/pages/booking/ClassesPage.tsx`):

- El administrador tiene el botón **Cupos** en cada clase programada, sin entrar al detalle. El backend sigue validando RN-10: no menos que las reservas ni más que el tope.
- La lista se vuelve a consultar sola cada 30 s con la pestaña visible y al volver a ella: el alumno ve las clases nuevas y los cupos cambiados sin buscar de nuevo.

**Índice** (`internal/application/indexer.go`): para que el alumno vea enseguida las clases nuevas sin que la caché guarde un resultado vacío, el indexer invalida la caché de búsquedas al aplicar y otra vez 1,5 s después. Ver [CORRECCION_BUSQUEDA.md](CORRECCION_BUSQUEDA.md) §3.

## 3. Verificación

El 14/11/2026, con el stack de `make demo` y los datos de `scripts/cargar-datos-demo.go`, a través del gateway y con los pasos que hace la pantalla:

| Prueba | Resultado |
|---|---|
| Crear la actividad Pilates, agregarle un horario y generar | 201, 201 y 2 clases nuevas |
| La alumna busca «pila» | Visible a 1 s |
| Cambiar los cupos de una clase de Pilates de 10 a 13 | 200; la alumna ve 13 a los 1,5 s |
| Generar dos meses de clases (39 nuevas) | Retraso máximo del índice 0,64 s |

Tests: `internal/application/indexer_invalidacion_test.go` (invalidación inmediata por versión y una sola diferida). Build del frontend aprobado.

## 4. Cómo usarlo

1. **Actividades y horarios** → crear la actividad. Queda elegida.
2. Completar el horario (día, inicio, duración, profesor y cupos) y tocar **Agregar horario**. Sus clases ya quedan generadas.
3. Para cambiar los cupos de una clase: **Clases** → **Cupos** en la fila. Para cambiar el tope de la actividad: **Tope** en la tabla de actividades.
