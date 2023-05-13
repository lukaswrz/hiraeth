document.addEventListener('DOMContentLoaded', () => {
    const uploadForm = document.querySelector('form#upload');

    const bar = document.createElement('div');
    bar.classList.add('bar');

    const progress = document.createElement('div');
    progress.classList.add('progress');
    progress.style.width = '0%';

    const description = document.createElement('div');
    description.classList.add('description');

    progress.appendChild(description);

    bar.appendChild(progress);

    uploadForm.parentElement.appendChild(bar);

    uploadForm.addEventListener('submit', async event => {
        event.preventDefault();

        const fileElement = event.target.querySelector('input[type="file"]#file');

        if (fileElement.files.length !== 1) {
            return;
        }

        const file = fileElement.files[0];

        const chunkSize = parseInt(fileElement.dataset.chunkSize);

        const responseRawPrepare = await fetch('/prepare', {
            method: 'POST',
            body: JSON.stringify({
                password: event.target.elements.password.value || null,
                time: parseInt(event.target.elements.time.value),
                unit: event.target.elements.unit.value,
                filename: file.name
            }),
            headers: {
                'Content-Type': 'application/json',
                'Accept': 'application/json'
            }
        });

        if (!responseRawPrepare.ok) {
            throw new Error(`Server responded with code ${responseRawPrepare.status}.`);
        }

        const responsePrepare = await responseRawPrepare.json();

        if (typeof responsePrepare.uuid !== 'string') {
            throw new Error('Could not get UUID of file entry.');
        }

        const uuid = responsePrepare.uuid;

        for (let start = 0; start < file.size; start += chunkSize) {
            progress.style.width = `${start / file.size * 100}%`;
            description.innerText = `Sending chunk ${start / chunkSize + 1}/${file.size / chunkSize}`;

            const chunk = file.slice(start, start + chunkSize)

            const chunkFormData = new FormData();
            chunkFormData.set('chunk', chunk);

            const responseRawAppend = await fetch(`/append/${encodeURIComponent(uuid)}`, {
                method: 'POST',
                body: chunkFormData,
                headers: {
                    'Accept': 'application/json'
                }
            });

            if (!responseRawAppend.ok) {
                throw new Error(`Server responded with code ${responseRawAppend.status}.`);
            }

            await responseRawAppend.blob();
        }

        const responseRawFinish = await fetch(`/finish/${uuid}`, {
            method: 'POST',
            body: JSON.stringify({}),
            headers: {
                'Content-Type': 'application/json',
                'Accept': 'application/json'
            }
        });

        if (!responseRawFinish.ok) {
            throw new Error(`Server responded with code ${responseRawFinish.status}.`);
        }

        await responseRawFinish.json();

        fileElement.value = null;
        location.reload();
    });
});
