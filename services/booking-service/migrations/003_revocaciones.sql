-- Bajas que llegan desde Members (RN-04, RN-34, CL-12, CL-19; D-20, D-98).
-- Booking consume membresia.cancelada y usuario.desactivado y cancela las
-- reservas futuras que ya no quedan cubiertas.

-- Deduplicación por evento (CLAUDE.md regla 7): se inserta en la misma
-- transacción que el efecto; una reentrega encuentra la fila y no repite nada.
CREATE TABLE eventos_consumidos (
  event_id uuid PRIMARY KEY,
  event_type text NOT NULL CHECK (event_type IN ('membresia.cancelada', 'usuario.desactivado')),
  procesado_en timestamptz NOT NULL DEFAULT now()
);

-- Revocación durable por alumno. Cierra la carrera de CL-19: una reserva que
-- validó la vigencia con Members antes de la revocación y llega después a la
-- base se rechaza al encontrar esta fila (comparando registrada_en con el
-- momento de su validación). El consumidor y la reserva se serializan con el
-- mismo bloqueo por alumno.
CREATE TABLE revocaciones (
  id bigserial PRIMARY KEY,
  event_id uuid NOT NULL UNIQUE REFERENCES eventos_consumidos(event_id),
  alumno_id uuid NOT NULL,
  tipo text NOT NULL CHECK (tipo IN ('MEMBRESIA_CANCELADA', 'USUARIO_DESACTIVADO')),
  membresia_id uuid,
  ocurrida_en timestamptz NOT NULL,
  registrada_en timestamptz NOT NULL DEFAULT now(),
  reservas_canceladas integer NOT NULL CHECK (reservas_canceladas >= 0),
  CHECK ((tipo = 'MEMBRESIA_CANCELADA') = (membresia_id IS NOT NULL))
);
CREATE INDEX revocaciones_por_alumno ON revocaciones (alumno_id, registrada_en);

CREATE FUNCTION proteger_revocaciones() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Revocaciones inmutables' USING ERRCODE = '23514'; END;
$$;
CREATE TRIGGER revocaciones_inmutables BEFORE UPDATE OR DELETE ON revocaciones
  FOR EACH ROW EXECUTE FUNCTION proteger_revocaciones();
