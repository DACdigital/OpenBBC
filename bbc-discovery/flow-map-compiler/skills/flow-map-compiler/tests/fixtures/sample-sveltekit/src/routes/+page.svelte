<script>
  async function ping() {
    const res = await fetch('/api/ping');
    return res.json();
  }
</script>

<button on:click={ping}>Ping</button>
