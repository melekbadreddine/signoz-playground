import requests
import time
import random
import logging

# Configure logging
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

API_GATEWAY_URL = "http://api-gateway:8000/order"
PRODUCTS = ["product-1", "product-2", "product-3"]

def generate_load():
    logging.info("Starting load generator...")
    while True:
        try:
            product = random.choice(PRODUCTS)
            quantity = random.randint(1, 5)
            
            payload = {
                "product_id": product,
                "quantity": quantity
            }
            
            logging.info(f"Sending request: {payload}")
            response = requests.post(API_GATEWAY_URL, json=payload, timeout=5)
            
            if response.status_code == 200:
                logging.info(f"Success: {response.json()}")
            else:
                logging.warning(f"Failed: {response.status_code} - {response.text}")
                
        except Exception as e:
            logging.error(f"Error sending request: {e}")
            
        # Wait between 1 and 5 seconds
        time.sleep(random.uniform(1, 5))

if __name__ == "__main__":
    # Give the services some time to start up
    time.sleep(10)
    generate_load()
