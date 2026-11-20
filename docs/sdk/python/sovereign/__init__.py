"""
Sovereign Python SDK
==================

A comprehensive Python client library for the Sovereign Command & Control API.

Installation:
    pip install sovereign

Quick Start:
    from sovereign import SovereignClient
    
    client = SovereignClient('http://localhost:8080')
    client.login('admin', 'admin')
    
    agents = client.get_agents()
    for agent in agents:
        print(f"Agent {agent.hostname} is {agent.status}")

Advanced Usage:
    # Use context manager for automatic cleanup
    with SovereignClient('http://localhost:8080') as client:
        client.login('admin', 'admin')
        
        # Execute shell command
        task = client.execute_shell(agent_id, 'whoami')
        result = client.wait_for_task(task.id)
        print(result)
        
        # Take screenshot
        screenshot = client.take_screenshot(agent_id)
        client.download_file(screenshot.path, 'screenshot.png')
"""

__version__ = '2.0.0'
__author__ = 'Sovereign Team'

from .client import SovereignClient, SovereignError
from .models import Agent, Task, Listener, Credential, AuditLog, User

__all__ = [
    'SovereignClient',
    'SovereignError',
    'Agent',
    'Task',
    'Listener',
    'Credential',
    'AuditLog',
    'User',
]
