-- +goose Up
-- Catch-up (ship.Delivery.Earlier, Retried): attempts counts the
-- deliveries an inbound ship was handed to an agent in. Claim and Join add
-- one; an ack that requeues joined ships the agent never saw takes it
-- back. A delivery with a ship handed before tells the agent its earlier
-- turn didn't finish. An agent's outbound ship records the delivery it was
-- working in the thread (delivery_id), so a session can tell its own last
-- replies from another slot's.
ALTER TABLE ships ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0);

-- +goose Down
ALTER TABLE ships DROP COLUMN attempts;
