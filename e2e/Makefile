COMPOSE := docker compose -p openbbc-e2e -f $(CURDIR)/compose.yml

.PHONY: up down test e2e logs

up:
	$(COMPOSE) up -d --build --wait

down:
	$(COMPOSE) --profile jobs down -v --remove-orphans

test:
	cd $(CURDIR) && uv run pytest tests -q

# up -> test -> always down; exit status is the test run's.
e2e:
	@$(MAKE) up && $(MAKE) test; rc=$$?; $(MAKE) logs-on-fail rc=$$rc; $(MAKE) down; exit $$rc

logs:
	$(COMPOSE) --profile jobs logs --no-color

.PHONY: logs-on-fail
logs-on-fail:
	@if [ "$(rc)" != "0" ]; then $(COMPOSE) logs --no-color | tail -n 200; fi
