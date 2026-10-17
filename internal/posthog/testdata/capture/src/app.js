// The setup the dashboard's Setup page gives (Task 8): cookieless (memory
// persistence), page views on load and on history changes, page leave on,
// no session recording. api_host and the token come from the page.
import posthog from 'posthog-js';

posthog.init(window.POSTHOG_TOKEN, {
  api_host: window.POSTHOG_HOST,
  persistence: 'memory',
  capture_pageview: 'history_change',
  capture_pageleave: true,
  disable_session_recording: true,
});
window.posthog = posthog;
document.body.dataset.ready = 'yes';
