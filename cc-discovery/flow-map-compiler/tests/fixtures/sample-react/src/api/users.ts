export type User = {
  id: string;
  name: string;
  email: string;
};

export type UpdateUserInput = {
  name?: string;
  email?: string;
};

export async function updateUser(
  id: string,
  input: UpdateUserInput,
  token: string,
): Promise<User> {
  const res = await fetch(`/api/users/${id}`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw new Error(`updateUser failed: ${res.status}`);
  return (await res.json()) as User;
}
