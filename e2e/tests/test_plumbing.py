"""Suite plumbing: the ResourceFactory and the real-path dataset seed."""


def test_factory_names_are_unique(factory, run_id):
    prefix = f"e2e-{run_id}-"
    assert factory.name("probe") == f"{prefix}probe-1"
    assert factory.name("probe") == f"{prefix}probe-2"
    assert factory.name("other") == f"{prefix}other-1"

    a = factory.agent()
    b = factory.agent()
    assert a.agent_id != b.agent_id
    assert a.version_id != b.version_id
    assert a.backend_id != b.backend_id
    assert a.backend_name != b.backend_name
    assert a.backend_name.startswith(f"{prefix}kb-")
    assert b.backend_name.startswith(f"{prefix}kb-")


def test_seed_agent_and_dataset(seeded, factory, db, mcp):
    dvid = factory.dataset(seeded)

    dv = db.dataset_version(dvid)
    assert dv["status"] == "CLOSED"
    assert len(dv["session_ids"]) == 1
    session_id = dv["session_ids"][0]

    finals = db.final_assistant_message_ids(session_id)
    assert len(finals) == 2
    criteria = db.feedback_criteria(finals)
    assert criteria == {
        finals[0]: ["Answers the question"],
        finals[1]: ["Follows the drafting guide [[require:PATCHED-GUIDE]]"],
    }

    searches = [c for c in mcp.calls() if c["method"] == "tools/call" and c["tool"] == "search"]
    assert searches, "fake-mcp saw no tools/call search"
    for call in searches:
        assert call["headers"].get("authorization") == "Bearer e2e-static-key"
        assert call["headers"].get("x-test-identity") == "dataset-user"
