-- P18.11: an intent written in-app only (the flow kernel's note that a button did not take effect, P20.8)
ALTER TABLE intent ADD COLUMN in_app_only boolean NOT NULL DEFAULT false;
