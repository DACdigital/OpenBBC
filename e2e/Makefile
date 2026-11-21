COMPOSE := docker compose -p openbbc-e2e -f $(CURDIR)/compose.yml

.PHONY: up down test e2e logs

# The runner is under profile `jobs`, which `up --build` skips; build it explicitly
# so a stale runner image can't hide Dockerfile changes.
up:
	$(COMPOSE) --profile jobs build runner
	$(COMPOSE) up -d --build --wait

down:
	$(COMPOSE) --profile jobs down -v --remove-orphans

test:
	cd $(CURDIR)/fakes && uv run pytest -q
	cd $(CURDIR) && uv run pytest tests -q $(PYTEST_ARGS)

# up -> test -> always down (also on Ctrl-C/TERM); exit status is the test run's.
e2e:
	@rm -f $(CURDIR)/e2e-logs.txt
	@trap 'exit 130' INT TERM; trap '$(MAKE) down' EXIT; \
	$(MAKE) up && $(MAKE) test; rc=$$?; $(MAKE) logs-on-fail rc=$$rc; exit $$rc

logs:
	$(COMPOSE) --profile jobs logs --no-color

.PHONY: logs-on-fail
logs-on-fail:
	@if [ "$(rc)" != "0" ]; then \
		$(COMPOSE) --profile jobs logs --no-color > $(CURDIR)/e2e-logs.txt 2>&1; \
		tail -n 200 $(CURDIR)/e2e-logs.txt; \
	fi
