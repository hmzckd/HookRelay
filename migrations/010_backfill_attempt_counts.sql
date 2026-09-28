UPDATE deliveries d
SET attempt_count = (SELECT count(*) FROM delivery_attempts a WHERE a.delivery_id = d.id)
WHERE EXISTS (SELECT 1 FROM delivery_attempts a WHERE a.delivery_id = d.id)
