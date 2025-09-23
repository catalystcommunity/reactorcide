#!/usr/bin/env python3
"""
Mock Corndogs server for E2E testing.
This provides a minimal implementation of the Corndogs API for testing purposes.
"""

import json
import uuid
from datetime import datetime
from typing import Dict, List, Optional
from flask import Flask, request, jsonify
import threading
import time

app = Flask(__name__)

# In-memory storage for tasks
tasks: Dict[str, dict] = {}
task_queue: List[str] = []
lock = threading.Lock()

@app.route('/api/v1/tasks', methods=['POST'])
def create_task():
    """Create a new task."""
    data = request.json

    task_id = str(uuid.uuid4())
    task = {
        'uuid': task_id,
        'queue_name': data.get('queue_name', 'default'),
        'current_state': 'queued',
        'priority': data.get('priority', 0),
        'payload': data.get('payload', {}),
        'created_at': datetime.utcnow().isoformat(),
        'updated_at': datetime.utcnow().isoformat(),
    }

    with lock:
        tasks[task_id] = task
        task_queue.append(task_id)

    return jsonify(task), 201

@app.route('/api/v1/tasks/<task_id>', methods=['GET'])
def get_task(task_id):
    """Get task by ID."""
    with lock:
        task = tasks.get(task_id)

    if not task:
        return jsonify({'error': 'Task not found'}), 404

    return jsonify(task)

@app.route('/api/v1/tasks/<task_id>', methods=['PUT'])
def update_task(task_id):
    """Update task state."""
    data = request.json

    with lock:
        task = tasks.get(task_id)
        if not task:
            return jsonify({'error': 'Task not found'}), 404

        if 'current_state' in data:
            task['current_state'] = data['current_state']
        if 'payload' in data:
            task['payload'].update(data['payload'])

        task['updated_at'] = datetime.utcnow().isoformat()

    return jsonify(task)

@app.route('/api/v1/queues/<queue_name>/next', methods=['POST'])
def get_next_task(queue_name):
    """Get next task from queue."""
    with lock:
        # Find next task in requested queue
        for task_id in task_queue:
            task = tasks.get(task_id)
            if task and task['queue_name'] == queue_name and task['current_state'] == 'queued':
                # Mark as running
                task['current_state'] = 'running'
                task['updated_at'] = datetime.utcnow().isoformat()
                task_queue.remove(task_id)
                return jsonify(task)

    # No tasks available
    return jsonify(None), 204

@app.route('/api/v1/health', methods=['GET'])
def health():
    """Health check endpoint."""
    return jsonify({
        'status': 'OK',
        'tasks_count': len(tasks),
        'queue_length': len(task_queue)
    })

@app.route('/api/v1/tasks/<task_id>/cancel', methods=['POST'])
def cancel_task(task_id):
    """Cancel a task."""
    with lock:
        task = tasks.get(task_id)
        if not task:
            return jsonify({'error': 'Task not found'}), 404

        task['current_state'] = 'cancelled'
        task['updated_at'] = datetime.utcnow().isoformat()

        # Remove from queue if present
        if task_id in task_queue:
            task_queue.remove(task_id)

    return jsonify(task)

# Background task processor (simulates workers)
def background_processor():
    """Simulate task processing."""
    while True:
        time.sleep(2)

        with lock:
            # Auto-complete running tasks after a delay
            for task_id, task in tasks.items():
                if task['current_state'] == 'running':
                    # Check if task has been running for more than 5 seconds
                    created = datetime.fromisoformat(task['created_at'])
                    if (datetime.utcnow() - created).total_seconds() > 5:
                        # Simulate completion
                        task['current_state'] = 'completed'
                        task['updated_at'] = datetime.utcnow().isoformat()

if __name__ == '__main__':
    # Start background processor
    processor_thread = threading.Thread(target=background_processor, daemon=True)
    processor_thread.start()

    # Run Flask app
    app.run(host='0.0.0.0', port=9090, debug=False)