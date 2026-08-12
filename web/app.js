// Shared helpers for the poll-tergeist static pages (no build step, no framework).

// Poll id from the URL path:
//   /p/{id}         -> { id, isResults: false }
//   /p/{id}/results -> { id, isResults: true }
function pollIdFromPath() {
  const parts = window.location.pathname.split('/').filter(Boolean); // e.g. ["p", "abc123", "results"]
  const id = parts[1] || '';
  const isResults = parts[2] === 'results';
  return { id, isResults };
}

async function fetchPoll(id) {
  const res = await fetch(`/api/polls/${encodeURIComponent(id)}`, {
    credentials: 'same-origin',
  });
  if (!res.ok) {
    throw new Error(res.status === 404 ? 'Poll not found.' : `Failed to load poll (${res.status}).`);
  }
  return res.json();
}

async function createPoll(question, options) {
  const res = await fetch('/api/polls', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question, options }),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(body.error || `Failed to create poll (${res.status}).`);
  }
  return body;
}

async function submitVote(id, optionId) {
  const res = await fetch(`/api/polls/${encodeURIComponent(id)}/votes`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ option_id: optionId }),
  });
  const body = await res.json().catch(() => ({}));
  // 201 = fresh vote, 409 = already voted; both mean the vote is recorded.
  if (res.status === 201 || res.status === 409) {
    return body;
  }
  throw new Error(body.error || `Failed to vote (${res.status}).`);
}

function openTallyStream(id, onTally) {
  const source = new EventSource(`/api/polls/${encodeURIComponent(id)}/stream`);
  source.onmessage = (event) => {
    try {
      onTally(JSON.parse(event.data));
    } catch (err) {
      console.error('poll-tergeist: bad tally event', err);
    }
  };
  return source;
}
