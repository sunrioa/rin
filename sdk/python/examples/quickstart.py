import os

from rin_sdk import RinControlClient


client = RinControlClient(token=os.environ["RIN_CONTROL_TOKEN"])
print(client.list_worlds())
