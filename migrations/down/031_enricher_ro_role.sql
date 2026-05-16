-- 031_enricher_ro_role (down): role + 모든 grant 회수.
-- DROP ROLE 은 잔여 grant 가 있으면 실패 — 명시적으로 REVOKE 후 DROP.

REVOKE ALL ON contents          FROM enricher_ro;
REVOKE ALL ON enriched_contents FROM enricher_ro;
REVOKE ALL ON SCHEMA public     FROM enricher_ro;

DROP ROLE IF EXISTS enricher_ro;
