(function () {
    'use strict';

    document.addEventListener('DOMContentLoaded', () => {
        const button = document.getElementById('nextdash-logout-button');
        if (!button) return;
        button.addEventListener('click', async () => {
            button.disabled = true;
            try {
                const response = await window.nextDashFetch('/logout', { method: 'POST' });
                if (!response.ok) throw new Error('logout failed');
                window.location.assign('/login');
            } catch {
                button.disabled = false;
            }
        });
    });
})();
