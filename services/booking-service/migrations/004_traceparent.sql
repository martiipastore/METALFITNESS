-- D11 (ARCHITECTURE §11): la traza atraviesa la frontera asíncrona. El productor
-- guarda el traceparent W3C del span que originó el evento y el relay lo envía
-- en el sobre; el consumidor continúa la traza. Solo traceparent: nunca
-- tracestate ni baggage. Los eventos generados por triggers o procesos
-- programados (clase.actualizada, inasistencia.registrada) no tienen traza de
-- origen y quedan en NULL.
ALTER TABLE outbox ADD COLUMN traceparent text
  CHECK (traceparent IS NULL OR traceparent ~ '^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$');
