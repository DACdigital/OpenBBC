"use client";

import { useState } from "react";
import { updateUser } from "../../lib/api/users";

export default function ProfilePage() {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError(null);
    try {
      const token = localStorage.getItem("token") ?? "";
      const userId = localStorage.getItem("userId") ?? "";
      await updateUser(userId, { name, email }, token);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Unknown error");
    } finally {
      setSaving(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Name" />
      <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" />
      <button type="submit" disabled={saving}>
        {saving ? "Saving..." : "Save"}
      </button>
      {error && <p>{error}</p>}
    </form>
  );
}
