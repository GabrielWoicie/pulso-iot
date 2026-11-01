BEGIN;
ALTER TABLE pulso_iot.devices
 ADD COLUMN device_mode varchar(16) NOT NULL DEFAULT 'physical'
 CONSTRAINT devices_device_mode_check CHECK (device_mode IN ('physical', 'simulated'));
COMMIT;
